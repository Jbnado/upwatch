package checker_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// serveOnce sobe um listener que entrega cada conexão ao handler.
func serveOnce(t *testing.T, handler func(net.Conn)) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				handler(conn)
			}()
		}
	}()
	return ln.Addr().String()
}

// fakePostgresError responde à abertura de sessão com um erro do
// servidor contendo msg. Recusa TLS antes, como um servidor sem SSL.
func fakePostgresError(msg string) func(net.Conn) {
	return func(conn net.Conn) {
		for {
			var size int32
			if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
				return
			}
			body := make([]byte, size-4)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			// 80877103 é o código do SSLRequest; 80877104, GSSENCRequest.
			code := binary.BigEndian.Uint32(body[:4])
			if code == 80877103 || code == 80877104 {
				_, _ = conn.Write([]byte("N"))
				continue
			}
			break
		}

		var fields bytes.Buffer
		for _, f := range []string{"SFATAL", "C28P01", "M" + msg} {
			fields.WriteString(f)
			fields.WriteByte(0)
		}
		fields.WriteByte(0)

		var pkt bytes.Buffer
		pkt.WriteByte('E')
		_ = binary.Write(&pkt, binary.BigEndian, int32(fields.Len()+4))
		pkt.Write(fields.Bytes())
		_, _ = conn.Write(pkt.Bytes())
	}
}

// fakeMySQLError responde à conexão com um pacote de erro no lugar do
// handshake, contendo msg.
func fakeMySQLError(msg string) func(net.Conn) {
	return func(conn net.Conn) {
		payload := append([]byte{0xff, 0x15, 0x04}, msg...) // 1045: acesso negado
		head := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), 0}
		_, _ = conn.Write(append(head, payload...))
	}
}

func sqlMonitor(typ domain.MonitorType, target, cfg string) domain.Monitor {
	return domain.Monitor{
		ID: 1, Name: "banco", Type: typ, Target: target,
		Interval: time.Minute, Timeout: 3 * time.Second,
		ConfirmationThreshold: 1, Config: json.RawMessage(cfg),
	}
}

type sqlCase struct {
	name    string
	typ     domain.MonitorType
	checker checker.Checker
	// fakeError monta um servidor falso que recusa a sessão com msg.
	fakeError func(msg string) func(net.Conn)
	validTLS  string
	tlsField  string
}

func sqlCases() []sqlCase {
	return []sqlCase{
		{"postgres", domain.MonitorPostgres, checker.NewPostgres(), fakePostgresError, "require", "sslmode"},
		{"mysql", domain.MonitorMySQL, checker.NewMySQL(), fakeMySQLError, "skip-verify", "tls"},
	}
}

func TestSQLTypes(t *testing.T) {
	for _, tc := range sqlCases() {
		if got := tc.checker.Type(); got != tc.typ {
			t.Errorf("%s: Type() = %v, want %v", tc.name, got, tc.typ)
		}
	}
}

func TestSQLValidateConfig(t *testing.T) {
	for _, tc := range sqlCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, ok := range []string{
				`{"username":"monitor"}`,
				`{"username":"monitor","password":"x","database":"app","query":"SELECT count(*) FROM pg_stat_activity"}`,
				`{"username":"monitor","` + tc.tlsField + `":"` + tc.validTLS + `"}`,
			} {
				if err := tc.checker.ValidateConfig(json.RawMessage(ok)); err != nil {
					t.Errorf("ValidateConfig(%s) returned unexpected error: %v", ok, err)
				}
			}

			for name, bad := range map[string]string{
				"vazia":          ``,
				"json inválido":  `{`,
				"sem usuário":    `{"password":"x"}`,
				"tls inventado":  `{"username":"u","` + tc.tlsField + `":"talvez"}`,
				"consulta vazia": `{"username":"u","query":"   "}`,
				"consulta longa": `{"username":"u","query":"SELECT ` + strings.Repeat("1", checker.MaxSQLQueryLength) + `"}`,
			} {
				if err := tc.checker.ValidateConfig(json.RawMessage(bad)); err == nil {
					t.Errorf("%s: ValidateConfig(%s) accepted", name, bad)
				}
			}
		})
	}
}

func sqlQueryConfig(t *testing.T, query string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"username": "u", "query": query})
	if err != nil {
		t.Fatalf("Marshal returned unexpected error: %v", err)
	}
	return raw
}

// A transação somente leitura é a proteção de fato, mas não a única: um
// comando que não começa por leitura não tem motivo para estar num
// monitor, e recusá-lo no cadastro tira do caminho tudo o que a
// transação não segura — COPY para arquivo, DO com código procedural,
// SET que mexe na própria transação. Comentário e espaço na frente não
// escondem o comando; o comentário executável do MySQL (/*! */) não conta
// como comentário, porque o servidor executa o que está dentro.
func TestSQLValidateConfigOnlyAcceptsReadingQueries(t *testing.T) {
	for _, tc := range sqlCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, ok := range []string{
				"SELECT 1",
				"  select count(*) FROM pg_stat_activity",
				"-- quantos workers\nSELECT 1",
				"/* comentário */ WITH x AS (SELECT 1) SELECT * FROM x",
				"# comentário do MySQL\nSELECT 1",
				"SHOW server_version",
				"VALUES (1)",
				"EXPLAIN SELECT 1",
				"(SELECT 1)",
			} {
				if err := tc.checker.ValidateConfig(sqlQueryConfig(t, ok)); err != nil {
					t.Errorf("ValidateConfig(%q) returned unexpected error: %v", ok, err)
				}
			}

			for _, bad := range []string{
				"DELETE FROM contas",
				"  insert into contas values (1)",
				"-- só um comentário\nUPDATE contas SET saldo = 0",
				"/* SELECT */ DROP TABLE contas",
				"/*! DELETE FROM contas */ SELECT 1",
				"/*M! DELETE FROM contas */ SELECT 1",
				"DO $$ BEGIN DELETE FROM contas; END $$",
				"COPY (SELECT 1) TO '/tmp/x'",
				"SET TRANSACTION READ WRITE",
				"CALL limpar()",
				"-- sem fim",
				"/* sem fim",
			} {
				if err := tc.checker.ValidateConfig(sqlQueryConfig(t, bad)); err == nil {
					t.Errorf("ValidateConfig(%q) accepted", bad)
				}
			}
		})
	}
}

// INTO OUTFILE e INTO DUMPFILE escrevem um arquivo no servidor e passam
// pela transação somente leitura do MySQL: conferido contra o 8.4, um
// usuário com o privilégio FILE grava o arquivo mesmo dentro de START
// TRANSACTION READ ONLY. As palavras são recusadas em qualquer posição,
// inclusive dentro de comentário executável; palavra-chave do MySQL não
// tem grafia alternativa.
func TestMySQLValidateConfigRejectsWritingFiles(t *testing.T) {
	for _, bad := range []string{
		"SELECT 1 INTO OUTFILE '/var/lib/mysql-files/x'",
		"SELECT token FROM api_keys INTO dumpfile '/tmp/x'",
		"SELECT 1 /*!INTO OUTFILE '/tmp/x'*/",
		"SELECT 1 INTO\n\tOutFile '/tmp/x'",
	} {
		if err := checker.NewMySQL().ValidateConfig(sqlQueryConfig(t, bad)); err == nil {
			t.Errorf("ValidateConfig(%q) accepted", bad)
		}
	}
}

func TestSQLCheckDownWhenPortIsClosed(t *testing.T) {
	for _, tc := range sqlCases() {
		res := tc.checker.Check(context.Background(),
			sqlMonitor(tc.typ, closedPort(t), `{"username":"u","password":"p"}`))

		if res.Status != domain.StatusDown {
			t.Errorf("%s: Status = %v, want down", tc.name, res.Status)
		}
	}
}

func TestSQLCheckRejectsMalformedTarget(t *testing.T) {
	for _, tc := range sqlCases() {
		res := tc.checker.Check(context.Background(),
			sqlMonitor(tc.typ, "banco-sem-porta", `{"username":"u"}`))

		if res.Status != domain.StatusDown || !strings.Contains(res.Message, "host:porta") {
			t.Errorf("%s: got %v %q, want down explaining the host:port format", tc.name, res.Status, res.Message)
		}
	}
}

// A causa vai para o painel, para o aviso no canal e para o histórico
// exportado, lidos por quem não tem acesso ao banco. O texto de erro do
// servidor não entra nela: ele repete o que recebeu (a senha, ecoada por
// um proxy) e o que leu (o valor de uma linha, num erro de conversão), e
// assim a causa viraria um canal de leitura do banco. Fica a etapa, a
// classe da falha e o código, que bastam para agir; o texto inteiro vai
// só para o log do servidor.
func TestSQLCheckCauseDoesNotCarryTheServerText(t *testing.T) {
	const secret = "s3nha/do@banco"
	for _, tc := range sqlCases() {
		t.Run(tc.name, func(t *testing.T) {
			addr := serveOnce(t, tc.fakeError("senha recusada: "+secret+" (s3nha%2Fdo%40banco), linha: tok-secreto-da-linha"))

			res := tc.checker.Check(context.Background(),
				sqlMonitor(tc.typ, addr, `{"username":"u","password":"`+secret+`"}`))

			if res.Status != domain.StatusDown {
				t.Fatalf("Status = %v, want down", res.Status)
			}
			for _, leak := range []string{secret, "s3nha%2Fdo%40banco", "tok-secreto", "senha recusada"} {
				if strings.Contains(res.Message, leak) {
					t.Errorf("Message carries the server text %q: %q", leak, res.Message)
				}
			}
			if !strings.Contains(res.Message, "autenticação") {
				t.Errorf("Message = %q, want the failure class (authentication)", res.Message)
			}
		})
	}
}

// O alvo é um host:porta de rede. Um host com barra vira socket Unix no
// driver do PostgreSQL, e com vírgula, uma lista de servidores tentados em
// sequência: os dois levam o monitor a lugares que o cadastro não mostra
// — o socket local do próprio host do UpWatch, um segundo servidor
// escondido depois da vírgula. O erro aparece no cadastro, não só na
// primeira verificação.
func TestProtocolTargetMustBeASingleNetworkHost(t *testing.T) {
	reg, err := checker.NewRegistry(checker.NewRedis(), checker.NewPostgres(), checker.NewMySQL())
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	for _, typ := range []domain.MonitorType{domain.MonitorRedis, domain.MonitorPostgres, domain.MonitorMySQL} {
		for _, target := range []string{
			"/var/run/postgresql:5432",
			"/tmp/.s.PGSQL.5432:5432",
			"db1,db2:5432",
			"db1:5432,db2",
		} {
			m := sqlMonitor(typ, target, `{"username":"u"}`)
			if err := reg.Validate(m); err == nil {
				t.Errorf("%s: Validate accepted target %q", typ, target)
			}
			if res := reg.Check(context.Background(), m); res.Status != domain.StatusDown {
				t.Errorf("%s: Check(%q) = %v, want down", typ, target, res.Status)
			}
		}

		if err := reg.Validate(sqlMonitor(typ, "db.interno:5432", `{"username":"u"}`)); err != nil {
			t.Errorf("%s: Validate rejected a plain host:port: %v", typ, err)
		}
	}
}
