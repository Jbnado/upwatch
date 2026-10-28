package checker_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Rodam quando as variáveis apontam para bancos descartáveis; sem elas o
// teste se anuncia como pulado. A do PostgreSQL é a mesma da suíte do
// store (ver CONTRIBUTING.md). Para o MySQL:
//
//	docker run -d --rm --name mysql -e MYSQL_ROOT_PASSWORD=root \
//	  -e MYSQL_USER=upwatch -e MYSQL_PASSWORD=upwatch -e MYSQL_DATABASE=upwatch \
//	  -p 53306:3306 mysql:8.4
//	UPWATCH_TEST_MYSQL_DSN='upwatch:upwatch@tcp(127.0.0.1:53306)/upwatch' go test ./internal/checker/...
//
// Os testes criam e apagam a tabela upwatch_probe. No PostgreSQL, com DSN
// de superusuário, criam também o papel upwatch_monitor, só com LOGIN.
const (
	postgresDSNEnv = "UPWATCH_TEST_POSTGRES_DSN"
	mysqlDSNEnv    = "UPWATCH_TEST_MYSQL_DSN"

	// loginOnlyRole é o papel do PostgreSQL que os testes criam para o
	// monitor, quando a DSN é de superusuário.
	loginOnlyRole     = "upwatch_monitor"
	loginOnlyPassword = "senha-do-papel-de-login"
)

// liveSQL descreve um banco real: como o monitor o enxerga e uma conexão
// administrativa para preparar o cenário.
type liveSQL struct {
	typ      domain.MonitorType
	checker  checker.Checker
	target   string
	user     string
	password string
	database string
	admin    *sql.DB
	// exec roda um comando administrativo fora do checker.
	exec func(t *testing.T, stmt string)
	// count lê o número de linhas da tabela de prova.
	count func(t *testing.T) int
	// running conta as sessões, fora a administrativa, executando um
	// comando que contém marker.
	running func(t *testing.T, marker string) int
	// sleep é uma consulta que dorme os segundos pedidos e carrega marker
	// no texto, para ser achada na lista de sessões.
	sleep func(seconds int, marker string) string
	// grant dá ao usuário do monitor todos os direitos sobre a tabela, para
	// a recusa de escrita vir da transação e não da falta de permissão.
	grant func(t *testing.T, table string)
	// adminUser e adminPassword são as credenciais da DSN, para os testes
	// que precisam do monitor com o usuário privilegiado.
	adminUser, adminPassword string
}

// waitNoneRunning espera até não sobrar sessão executando marker, ou falha.
func (l liveSQL) waitNoneRunning(t *testing.T, marker string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		n := l.running(t, marker)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d session(s) still running %q on the server %v after the check returned", n, marker, within)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l liveSQL) monitor(cfg map[string]any) domain.Monitor {
	base := map[string]any{"username": l.user, "password": l.password, "database": l.database}
	for k, v := range cfg {
		base[k] = v
	}
	raw, _ := json.Marshal(base)
	return sqlMonitor(l.typ, l.target, string(raw))
}

// livePostgresConn abre uma conexão administrativa com a DSN de teste.
func livePostgresConn(t *testing.T) *pgx.Conn {
	t.Helper()

	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s não definida; pulando teste contra PostgreSQL real", postgresDSNEnv)
	}
	cc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("%s inválida: %v", postgresDSNEnv, err)
	}
	conn, err := pgx.ConnectConfig(context.Background(), cc)
	if err != nil {
		t.Fatalf("conectando em %s: %v", postgresDSNEnv, err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func livePostgres(t *testing.T) liveSQL {
	t.Helper()

	conn := livePostgresConn(t)
	cc := conn.Config()

	exec := func(t *testing.T, stmt string) {
		t.Helper()
		if _, err := conn.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// O monitor usa um papel só com LOGIN, o recomendado no README: com o
	// superusuário da DSN, consulta personalizada é recusada (ver
	// TestPostgresLiveSuperuserCustomQueryIsRefused). Sem permissão de
	// criar papel, fica o usuário da DSN.
	user, password := cc.User, cc.Password
	grant := func(*testing.T, string) {}
	var super string
	if err := conn.QueryRow(context.Background(), "SELECT current_setting('is_superuser')").Scan(&super); err != nil {
		t.Fatalf("reading is_superuser: %v", err)
	}
	if super == "on" {
		exec(t, "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '"+loginOnlyRole+"') "+
			"THEN CREATE ROLE "+loginOnlyRole+" LOGIN; END IF; END $$")
		exec(t, "ALTER ROLE "+loginOnlyRole+" LOGIN NOSUPERUSER PASSWORD '"+loginOnlyPassword+"'")
		user, password = loginOnlyRole, loginOnlyPassword
		grant = func(t *testing.T, table string) {
			t.Helper()
			exec(t, "GRANT ALL ON "+table+" TO "+loginOnlyRole)
		}
	}

	return liveSQL{
		typ: domain.MonitorPostgres, checker: checker.NewPostgres(),
		target: net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		user:   user, password: password, database: cc.Database,
		adminUser: cc.User, adminPassword: cc.Password,
		exec: exec, grant: grant,
		count: func(t *testing.T) int {
			var n int
			if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM upwatch_probe").Scan(&n); err != nil {
				t.Fatalf("counting probe rows: %v", err)
			}
			return n
		},
		running: func(t *testing.T, marker string) int {
			var n int
			if err := conn.QueryRow(context.Background(),
				"SELECT count(*) FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND state <> 'idle' AND query LIKE '%' || $1 || '%'",
				marker).Scan(&n); err != nil {
				t.Fatalf("reading pg_stat_activity: %v", err)
			}
			return n
		},
		sleep: func(seconds int, marker string) string {
			return fmt.Sprintf("SELECT pg_sleep(%d), '%s'", seconds, marker)
		},
	}
}

func liveMySQL(t *testing.T) liveSQL {
	t.Helper()

	dsn := os.Getenv(mysqlDSNEnv)
	if dsn == "" {
		t.Skipf("%s não definida; pulando teste contra MySQL real", mysqlDSNEnv)
	}
	dc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("%s inválida: %v", mysqlDSNEnv, err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("abrindo %s: %v", mysqlDSNEnv, err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("%s não responde: %v", mysqlDSNEnv, err)
	}

	return liveSQL{
		typ: domain.MonitorMySQL, checker: checker.NewMySQL(),
		target: dc.Addr, user: dc.User, password: dc.Passwd, database: dc.DBName,
		adminUser: dc.User, adminPassword: dc.Passwd,
		admin: db,
		// O monitor usa o próprio usuário da DSN, dono das tabelas.
		grant: func(*testing.T, string) {},
		exec: func(t *testing.T, stmt string) {
			t.Helper()
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		},
		count: func(t *testing.T) int {
			var n int
			if err := db.QueryRow("SELECT count(*) FROM upwatch_probe").Scan(&n); err != nil {
				t.Fatalf("counting probe rows: %v", err)
			}
			return n
		},
		running: func(t *testing.T, marker string) int {
			var n int
			// SHOW PROCESSLIST em forma de tabela, para filtrar.
			if err := db.QueryRow(
				"SELECT count(*) FROM information_schema.processlist WHERE id <> CONNECTION_ID() AND command = 'Query' AND info LIKE CONCAT('%', ?, '%')",
				marker).Scan(&n); err != nil {
				t.Fatalf("reading the process list: %v", err)
			}
			return n
		},
		sleep: func(seconds int, marker string) string {
			return fmt.Sprintf("SELECT SLEEP(%d), '%s'", seconds, marker)
		},
	}
}

func eachLiveSQL(t *testing.T, fn func(t *testing.T, l liveSQL)) {
	t.Run("postgres", func(t *testing.T) { fn(t, livePostgres(t)) })
	t.Run("mysql", func(t *testing.T) { fn(t, liveMySQL(t)) })
}

func TestSQLLiveDefaultQueryIsUp(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		res := l.checker.Check(context.Background(), l.monitor(nil))

		if res.Status != domain.StatusUp {
			t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
		}
	})
}

func TestSQLLiveCustomQuery(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		res := l.checker.Check(context.Background(),
			l.monitor(map[string]any{"query": "SELECT count(*) FROM information_schema.tables"}))

		if res.Status != domain.StatusUp {
			t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
		}
	})
}

func TestSQLLiveFailingQueryIsDown(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		res := l.checker.Check(context.Background(),
			l.monitor(map[string]any{"query": "SELECT * FROM tabela_que_nao_existe"}))

		if res.Status != domain.StatusDown || !strings.HasPrefix(res.Message, "consulta: ") {
			t.Fatalf("got %v %q, want down at the query stage", res.Status, res.Message)
		}
	})
}

// A consulta roda a cada minuto com uma credencial de produção. Mesmo
// que o usuário possa escrever — contra a recomendação —, a verificação
// não altera dado: nem por um comando de escrita direto, nem por um
// segundo comando emendado depois de um SELECT inocente.
//
// Os três últimos se escondem atrás de algo que não é escrita: WITH e
// EXPLAIN ANALYZE passam pela regra de começar com leitura e só a
// transação os segura; o DO com SET TRANSACTION READ WRITE tenta reabrir a
// escrita por dentro. Rodaram contra os servidores antes de a regra
// existir, e a transação segurou todos: no PostgreSQL, SQLSTATE 25006 e,
// no DO, 25001 ("transaction read-write mode must be set before any
// query"); no MySQL, 1792 no EXPLAIN ANALYZE e erro de sintaxe nos
// outros dois. Ficam aqui para a transação continuar provada sozinha.
func TestSQLLiveQueryCannotWrite(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		l.exec(t, "DROP TABLE IF EXISTS upwatch_probe")
		l.exec(t, "CREATE TABLE upwatch_probe (id INT)")
		l.exec(t, "INSERT INTO upwatch_probe (id) VALUES (1)")
		l.grant(t, "upwatch_probe")
		t.Cleanup(func() { l.exec(t, "DROP TABLE IF EXISTS upwatch_probe") })

		for _, q := range []string{
			"INSERT INTO upwatch_probe (id) VALUES (2)",
			"DELETE FROM upwatch_probe",
			"SELECT 1; DELETE FROM upwatch_probe",
			"WITH d AS (DELETE FROM upwatch_probe RETURNING id) SELECT id FROM d",
			"EXPLAIN ANALYZE DELETE FROM upwatch_probe",
			"DO $$ BEGIN SET TRANSACTION READ WRITE; INSERT INTO upwatch_probe (id) VALUES (3); END $$",
		} {
			res := l.checker.Check(context.Background(), l.monitor(map[string]any{"query": q}))
			if res.Status != domain.StatusDown {
				t.Errorf("%q: Status = %v, want down", q, res.Status)
			}
		}

		if n := l.count(t); n != 1 {
			t.Errorf("probe table has %d rows after the checks, want 1 (untouched)", n)
		}
	})
}

func TestSQLLiveWrongPasswordIsDownWithoutLeaking(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		const wrong = "senha-errada/que@nao-pode-vazar"
		l.password = wrong

		res := l.checker.Check(context.Background(), l.monitor(nil))

		if res.Status != domain.StatusDown {
			t.Fatalf("Status = %v, want down", res.Status)
		}
		if strings.Contains(res.Message, wrong) || strings.Contains(res.Message, "senha-errada") {
			t.Errorf("Message leaks the password: %q", res.Message)
		}
	})
}

// singleConnProxy repassa ao alvo só a primeira conexão e recusa as
// seguintes. É o que acontece atrás de um pooler ou firewall que não
// encaminha o pedido de cancelamento do PostgreSQL, que chega por uma
// conexão nova: o cancelamento do lado do cliente some, e só o que foi
// combinado com o servidor na própria sessão continua valendo.
func singleConnProxy(t *testing.T, target string) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		first := true
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			if !first {
				client.Close()
				continue
			}
			first = false
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				client.Close()
				continue
			}
			go func() {
				defer upstream.Close()
				_, _ = io.Copy(upstream, client)
			}()
			go func() {
				defer client.Close()
				_, _ = io.Copy(client, upstream)
			}()
		}
	}()
	return ln.Addr().String()
}

// O timeout do monitor fecha o socket, mas não para a consulta no
// servidor: um pg_sleep ou uma consulta pesada seguiria rodando, e a cada
// intervalo o monitor somaria mais uma sessão viva até esgotar o limite de
// conexões do banco que ele devia vigiar. Quando a verificação termina,
// nada dela pode continuar executando lá — mesmo que o pedido de
// cancelamento do cliente não chegue, por isso o proxy.
func TestSQLLiveTimeoutLeavesNothingRunning(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		const marker = "upwatch-sleep-probe"
		l.target = singleConnProxy(t, l.target)
		cfg := map[string]any{"query": l.sleep(30, marker)}
		if l.typ == domain.MonitorPostgres {
			// O prefer abriria uma segunda conexão depois da recusa do
			// TLS, e o proxy a recusaria.
			cfg["sslmode"] = "disable"
		}
		m := l.monitor(cfg)
		m.Timeout = 2 * time.Second

		res := l.checker.Check(context.Background(), m)

		if res.Status != domain.StatusDown {
			t.Fatalf("Status = %v (%s), want down: the query sleeps past the timeout", res.Status, res.Message)
		}
		l.waitNoneRunning(t, marker, 2*time.Second)
	})
}

// Um erro de conversão repete o valor que não converteu. Sem a causa
// genérica, "SELECT CAST(token AS int) FROM api_keys" num monitor punha o
// token no painel e no aviso do canal para quem só cadastra monitores. No
// MySQL o CAST não falha; o EXTRACTVALUE faz o mesmo papel, e é a forma
// clássica de extrair dado por mensagem de erro.
func TestSQLLiveQueryErrorDoesNotLeakRowData(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		const token = "tok-secreto-da-linha"
		l.exec(t, "DROP TABLE IF EXISTS upwatch_secret")
		l.exec(t, "CREATE TABLE upwatch_secret (token VARCHAR(64))")
		l.exec(t, "INSERT INTO upwatch_secret (token) VALUES ('"+token+"')")
		l.grant(t, "upwatch_secret")
		t.Cleanup(func() { l.exec(t, "DROP TABLE IF EXISTS upwatch_secret") })

		query := "SELECT CAST(token AS int) FROM upwatch_secret"
		if l.typ == domain.MonitorMySQL {
			query = "SELECT EXTRACTVALUE(1, CONCAT(0x7e, (SELECT token FROM upwatch_secret LIMIT 1)))"
		}
		res := l.checker.Check(context.Background(), l.monitor(map[string]any{"query": query}))

		if res.Status != domain.StatusDown {
			t.Fatalf("Status = %v (%s), want down", res.Status, res.Message)
		}
		if strings.Contains(res.Message, "secreto") {
			t.Errorf("Message leaks the row value: %q", res.Message)
		}
		if !strings.HasPrefix(res.Message, "consulta: ") || !strings.Contains(res.Message, "SQLSTATE") {
			t.Errorf("Message = %q, want the stage and the SQLSTATE", res.Message)
		}
	})
}

// INTO OUTFILE grava um arquivo no servidor e passa pela transação somente
// leitura: contra o MySQL 8.4, um usuário com o privilégio FILE grava o
// arquivo mesmo dentro de START TRANSACTION READ ONLY. O usuário
// recomendado, sem FILE, já falha por privilégio; o teste só mostra a
// regra do checker quando a DSN usa um usuário com FILE, como o root da CI.
func TestMySQLLiveSelectIntoOutfileDoesNotWrite(t *testing.T) {
	l := liveMySQL(t)

	var dir sql.NullString
	if err := l.admin.QueryRow("SELECT @@secure_file_priv").Scan(&dir); err != nil {
		t.Fatalf("reading secure_file_priv: %v", err)
	}
	path := dir.String + fmt.Sprintf("upwatch-outfile-%d", time.Now().UnixNano())

	res := l.checker.Check(context.Background(),
		l.monitor(map[string]any{"query": "SELECT 1 INTO OUTFILE '" + path + "'"}))

	if res.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", res.Status)
	}
	var written sql.NullString
	if err := l.admin.QueryRow("SELECT LOAD_FILE(?)", path).Scan(&written); err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	if written.Valid {
		t.Errorf("the check wrote %s on the server", path)
	}
}

// Uma consulta que devolve muitas linhas não é lida inteira: cada
// verificação traria o resultado todo para o UpWatch e seguraria o banco
// até o timeout, acusando queda num banco que respondeu. Saber que a
// consulta roda e devolve linhas basta, e as primeiras dizem isso. Nada
// pode ficar rodando no servidor depois.
func TestSQLLiveReadsOnlyTheFirstRows(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		const marker = "upwatch-rows-probe"
		query := "SELECT generate_series(1, 1000000000), '" + marker + "'"
		if l.typ == domain.MonitorMySQL {
			query = "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 1000) " +
				"SELECT 1, '" + marker + "' FROM n a, n b, n c"
		}

		res := l.checker.Check(context.Background(), l.monitor(map[string]any{"query": query}))

		if res.Status != domain.StatusUp {
			t.Fatalf("Status = %v (%s), want up after reading the first rows", res.Status, res.Message)
		}
		l.waitNoneRunning(t, marker, 3*time.Second)
	})
}

// A transação somente leitura não segura um superusuário: contra o
// PostgreSQL 17, SELECT lo_export(...) grava um arquivo no servidor dentro
// dela, e nenhuma lista de palavras proibidas o pega (U&"..." e
// query_to_xml('...') escondem o nome da função). Com superusuário, a
// consulta personalizada é recusada antes de rodar; a padrão, que é texto
// do UpWatch, continua, para quem só quer saber se o banco aceita sessão.
// Um papel só com LOGIN, o recomendado, roda a consulta personalizada
// normalmente.
func TestPostgresLiveSuperuserCustomQueryIsRefused(t *testing.T) {
	l := livePostgres(t)
	if l.user == l.adminUser {
		t.Skipf("%s não é de superusuário; nada a recusar", postgresDSNEnv)
	}
	ctx := context.Background()

	super := l
	super.user, super.password = l.adminUser, l.adminPassword

	var oid uint32
	admin := livePostgresConn(t)
	if err := admin.QueryRow(ctx, "SELECT lo_from_bytea(0, 'upwatch')").Scan(&oid); err != nil {
		t.Fatalf("creating the large object: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "SELECT lo_unlink($1)", oid) })
	path := fmt.Sprintf("/tmp/upwatch-lo-export-%d", time.Now().UnixNano())

	res := super.checker.Check(ctx, super.monitor(map[string]any{
		"query": fmt.Sprintf("SELECT lo_export(%d, '%s')", oid, path),
	}))

	if res.Status != domain.StatusDown || !strings.Contains(res.Message, "superusuário") {
		t.Errorf("superuser lo_export: got %v %q, want down refusing the superuser", res.Status, res.Message)
	}
	var written *int64
	if err := admin.QueryRow(ctx, "SELECT (pg_stat_file($1, true)).size", path).Scan(&written); err != nil {
		t.Fatalf("checking the file: %v", err)
	}
	if written != nil {
		t.Errorf("the check wrote %s on the server", path)
	}

	for _, q := range []string{"", "SELECT 1", "  select   1 "} {
		res := super.checker.Check(ctx, super.monitor(map[string]any{"query": q}))
		if res.Status != domain.StatusUp {
			t.Errorf("superuser with the default query %q: got %v %q, want up", q, res.Status, res.Message)
		}
	}

	res = l.checker.Check(ctx, l.monitor(map[string]any{"query": "SELECT count(*) FROM pg_stat_activity"}))
	if res.Status != domain.StatusUp {
		t.Errorf("LOGIN-only role with a custom query: got %v %q, want up", res.Status, res.Message)
	}
}
