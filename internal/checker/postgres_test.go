package checker_test

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// startupRecorder sobe um PostgreSQL falso que guarda os parâmetros de
// abertura de cada sessão e recusa a senha em seguida. Recusa TLS antes,
// como um servidor sem SSL.
func startupRecorder(t *testing.T) (string, <-chan map[string]string) {
	t.Helper()

	got := make(chan map[string]string, 4)
	addr := serveOnce(t, func(conn net.Conn) {
		be := pgproto3.NewBackend(conn, conn)
		for {
			msg, err := be.ReceiveStartupMessage()
			if err != nil {
				return
			}
			switch msg := msg.(type) {
			case *pgproto3.SSLRequest, *pgproto3.GSSEncRequest:
				if _, err := conn.Write([]byte("N")); err != nil {
					return
				}
				continue
			case *pgproto3.StartupMessage:
				params := make(map[string]string, len(msg.Parameters))
				for k, v := range msg.Parameters {
					params[k] = v
				}
				got <- params
			}
			break
		}
		be.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "senha recusada"})
		_ = be.Flush()
	})
	return addr, got
}

// O UpWatch roda num host que pode ter variáveis do libpq definidas para
// outra finalidade — um script de backup, a sessão de quem administra a
// máquina. Elas não podem mudar como o monitor conecta: PGSSLCERT e
// PGSSLROOTCERT apontando para arquivo que não existe derrubariam todos os
// monitores de PostgreSQL com erro de configuração; PGOPTIONS mudaria
// parâmetros da sessão no servidor monitorado (desligar o somente
// leitura, por exemplo); PGDATABASE trocaria o banco de quem deixou o
// campo vazio. A conexão sai só do que está no cadastro.
func TestPostgresIgnoresTheLibpqEnvironmentOfTheHost(t *testing.T) {
	t.Setenv("PGSSLCERT", "nao-existe/cliente.crt")
	t.Setenv("PGSSLKEY", "nao-existe/cliente.key")
	t.Setenv("PGSSLROOTCERT", "nao-existe/raiz.crt")
	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=off")
	t.Setenv("PGAPPNAME", "app-do-host")
	t.Setenv("PGDATABASE", "banco-do-host")
	t.Setenv("PGTZ", "America/Manaus")

	addr, startups := startupRecorder(t)
	m := sqlMonitor(domain.MonitorPostgres, addr, `{"username":"monitor","password":"p"}`)

	res := checker.NewPostgres().Check(context.Background(), m)

	if !strings.Contains(res.Message, "autenticação") {
		t.Fatalf("Message = %q, want the check to reach the server and fail authentication", res.Message)
	}
	var params map[string]string
	select {
	case params = <-startups:
	case <-time.After(time.Second):
		t.Fatal("the server never received a startup message")
	}
	if params["user"] != "monitor" {
		t.Errorf("user = %q, want monitor", params["user"])
	}
	for _, key := range []string{"options", "application_name", "database", "TimeZone", "timezone"} {
		if v, ok := params[key]; ok {
			t.Errorf("startup carries %s=%q, inherited from the host environment", key, v)
		}
	}
}

// stuckRollbackServer sobe um PostgreSQL falso que aceita a sessão, a
// transação e a consulta, e nunca responde ao ROLLBACK.
func stuckRollbackServer(t *testing.T) string {
	t.Helper()

	return serveOnce(t, func(conn net.Conn) {
		be := pgproto3.NewBackend(conn, conn)
		for {
			msg, err := be.ReceiveStartupMessage()
			if err != nil {
				return
			}
			if _, ok := msg.(*pgproto3.StartupMessage); ok {
				break
			}
			if _, ok := msg.(*pgproto3.CancelRequest); ok {
				return
			}
			if _, err := conn.Write([]byte("N")); err != nil {
				return
			}
		}
		be.Send(&pgproto3.AuthenticationOk{})
		be.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
		be.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if err := be.Flush(); err != nil {
			return
		}

		for {
			msg, err := be.Receive()
			if err != nil {
				return
			}
			switch msg := msg.(type) {
			case *pgproto3.Query:
				if strings.HasPrefix(strings.ToUpper(msg.String), "ROLLBACK") {
					// Lê até o cliente desistir, sem responder.
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
				_ = be.Flush()
			case *pgproto3.Parse:
				be.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				be.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				be.Send(&pgproto3.NoData{})
			case *pgproto3.Execute:
				be.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
			case *pgproto3.Sync:
				be.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
				_ = be.Flush()
			case *pgproto3.Terminate:
				return
			}
		}
	})
}

// O ROLLBACK roda depois da consulta, já fora do prazo dela. Feito sem
// prazo próprio, um servidor (ou proxy) que nunca o responde prende a
// verificação para sempre, e com ela a vaga do agendador: o monitor para
// de ser verificado sem nada dizer.
func TestPostgresRollbackDoesNotHangTheCheck(t *testing.T) {
	addr := stuckRollbackServer(t)
	m := sqlMonitor(domain.MonitorPostgres, addr, `{"username":"u","password":"p","sslmode":"disable"}`)
	m.Timeout = time.Second

	done := make(chan checker.Result, 1)
	start := time.Now()
	go func() { done <- checker.NewPostgres().Check(context.Background(), m) }()

	select {
	case res := <-done:
		if res.Status != domain.StatusUp {
			t.Errorf("Status = %v (%s), want up: the query itself answered", res.Status, res.Message)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("Check took %v, want it bounded by a short rollback deadline", elapsed)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Check is still waiting for the ROLLBACK")
	}
}
