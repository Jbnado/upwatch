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
// exportado. Um servidor (ou proxy na frente dele) que repete a senha no
// erro não pode fazê-la chegar a esses lugares. A senha tem caractere que
// muda quando codificado em URL, para pegar também a forma escapada.
func TestSQLCheckNeverLeaksThePasswordInTheCause(t *testing.T) {
	const secret = "s3nha/do@banco"
	for _, tc := range sqlCases() {
		t.Run(tc.name, func(t *testing.T) {
			addr := serveOnce(t, tc.fakeError("senha recusada: "+secret+" (s3nha%2Fdo%40banco)"))

			res := tc.checker.Check(context.Background(),
				sqlMonitor(tc.typ, addr, `{"username":"u","password":"`+secret+`"}`))

			if res.Status != domain.StatusDown {
				t.Fatalf("Status = %v, want down", res.Status)
			}
			if strings.Contains(res.Message, secret) || strings.Contains(res.Message, "s3nha%2Fdo%40banco") {
				t.Errorf("Message leaks the password: %q", res.Message)
			}
			if !strings.Contains(res.Message, "senha recusada") {
				t.Errorf("Message = %q, want the rest of the server error preserved", res.Message)
			}
		})
	}
}
