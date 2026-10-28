package checker_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

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
// Os testes criam e apagam a tabela upwatch_probe.
const (
	postgresDSNEnv = "UPWATCH_TEST_POSTGRES_DSN"
	mysqlDSNEnv    = "UPWATCH_TEST_MYSQL_DSN"
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
}

func (l liveSQL) monitor(cfg map[string]any) domain.Monitor {
	base := map[string]any{"username": l.user, "password": l.password, "database": l.database}
	for k, v := range cfg {
		base[k] = v
	}
	raw, _ := json.Marshal(base)
	return sqlMonitor(l.typ, l.target, string(raw))
}

func livePostgres(t *testing.T) liveSQL {
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

	exec := func(t *testing.T, stmt string) {
		t.Helper()
		if _, err := conn.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return liveSQL{
		typ: domain.MonitorPostgres, checker: checker.NewPostgres(),
		target: net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))),
		user:   cc.User, password: cc.Password, database: cc.Database,
		exec: exec,
		count: func(t *testing.T) int {
			var n int
			if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM upwatch_probe").Scan(&n); err != nil {
				t.Fatalf("counting probe rows: %v", err)
			}
			return n
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
		admin: db,
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
func TestSQLLiveQueryCannotWrite(t *testing.T) {
	eachLiveSQL(t, func(t *testing.T, l liveSQL) {
		l.exec(t, "DROP TABLE IF EXISTS upwatch_probe")
		l.exec(t, "CREATE TABLE upwatch_probe (id INT)")
		l.exec(t, "INSERT INTO upwatch_probe (id) VALUES (1)")
		t.Cleanup(func() { l.exec(t, "DROP TABLE IF EXISTS upwatch_probe") })

		for _, q := range []string{
			"INSERT INTO upwatch_probe (id) VALUES (2)",
			"DELETE FROM upwatch_probe",
			"SELECT 1; DELETE FROM upwatch_probe",
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
