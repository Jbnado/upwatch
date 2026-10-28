package checker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// fakeRedis é um servidor RESP mínimo: responde cada comando pelo
// handler e registra o que recebeu. Permite testar o checker sem um Redis
// de verdade, inclusive respostas que um Redis real raramente dá.
type fakeRedis struct {
	addr string

	mu       sync.Mutex
	received [][]string
}

// startFakeRedis sobe o servidor. O handler devolve a resposta já em RESP;
// vazio cai no padrão (+PONG para PING, +OK para o resto).
func startFakeRedis(t *testing.T, handler func(cmd []string) string) *fakeRedis {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	f := &fakeRedis{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, handler)
		}
	}()
	return f
}

func (f *fakeRedis) serve(conn net.Conn, handler func([]string) string) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		cmd, err := readRESPCommand(r)
		if err != nil || len(cmd) == 0 {
			return
		}
		cmd[0] = strings.ToUpper(cmd[0])
		f.mu.Lock()
		f.received = append(f.received, cmd)
		f.mu.Unlock()

		reply := ""
		// Como um Redis anterior ao 6: o cliente recua para AUTH, que é o
		// caminho que os testes de credencial exercitam.
		if cmd[0] == "HELLO" {
			reply = "-ERR unknown command 'HELLO'\r\n"
		}
		if reply == "" && handler != nil {
			reply = handler(cmd)
		}
		if reply == "" {
			reply = "+OK\r\n"
			if cmd[0] == "PING" {
				reply = "+PONG\r\n"
			}
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (f *fakeRedis) commands() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.received...)
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		head, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(head, "$")))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:size]))
	}
	return out, nil
}

func redisMonitor(target string, cfg string) domain.Monitor {
	return domain.Monitor{
		ID: 1, Name: "redis", Type: domain.MonitorRedis, Target: target,
		Interval: time.Minute, Timeout: 2 * time.Second,
		ConfirmationThreshold: 1, Config: json.RawMessage(cfg),
	}
}

func TestRedisTypeIsRedis(t *testing.T) {
	if got := checker.NewRedis().Type(); got != domain.MonitorRedis {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorRedis)
	}
}

func TestRedisValidateConfig(t *testing.T) {
	c := checker.NewRedis()

	for _, ok := range []string{
		``,
		`{}`,
		`{"password":"x","username":"monitor","db":2,"tls":true}`,
		`{"max_used_memory_bytes":1073741824}`,
		`{"assertions":[{"command":"SCARD","key":"rq:workers:default","op":"gte","value":1},
		  {"command":"LLEN","key":"rq:queue:default","op":"lt","value":500}]}`,
	} {
		if err := c.ValidateConfig(json.RawMessage(ok)); err != nil {
			t.Errorf("ValidateConfig(%s) returned unexpected error: %v", ok, err)
		}
	}

	many := make([]string, checker.MaxRedisAssertions+1)
	for i := range many {
		many[i] = `{"command":"DBSIZE","op":"gt","value":0}`
	}
	for name, bad := range map[string]string{
		"json inválido":         `{`,
		"banco negativo":        `{"db":-1}`,
		"memória negativa":      `{"max_used_memory_bytes":-1}`,
		"comando de escrita":    `{"assertions":[{"command":"FLUSHALL","op":"eq","value":0}]}`,
		"asserção sem operador": `{"assertions":[{"command":"DBSIZE","value":0}]}`,
		"asserções demais":      `{"assertions":[` + strings.Join(many, ",") + `]}`,
	} {
		if err := c.ValidateConfig(json.RawMessage(bad)); err == nil {
			t.Errorf("%s: ValidateConfig(%s) accepted", name, bad)
		}
	}
}

func TestRedisCheckUpOnPong(t *testing.T) {
	srv := startFakeRedis(t, nil)

	res := checker.NewRedis().Check(context.Background(), redisMonitor(srv.addr, `{}`))

	if res.Status != domain.StatusUp {
		t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
	}
}

// É a razão de o checker existir: um Redis carregando o dump do disco ou
// sem memória aceita TCP normalmente e, na prática, está fora do ar.
func TestRedisCheckDownWhenPingFails(t *testing.T) {
	srv := startFakeRedis(t, func(cmd []string) string {
		if cmd[0] == "PING" {
			return "-LOADING Redis is loading the dataset in memory\r\n"
		}
		return ""
	})

	res := checker.NewRedis().Check(context.Background(), redisMonitor(srv.addr, `{}`))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if !strings.Contains(res.Message, "LOADING") {
		t.Errorf("Message = %q, want the server error", res.Message)
	}
}

func TestRedisCheckDownWhenPortIsClosed(t *testing.T) {
	res := checker.NewRedis().Check(context.Background(), redisMonitor(closedPort(t), `{}`))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
}

func TestRedisCheckRejectsMalformedTarget(t *testing.T) {
	res := checker.NewRedis().Check(context.Background(), redisMonitor("redis-sem-porta", `{}`))

	if res.Status != domain.StatusDown || !strings.Contains(res.Message, "host:porta") {
		t.Fatalf("got %v %q, want down explaining the host:port format", res.Status, res.Message)
	}
}

// O caso real: workers do RQ mortos deixam o Redis respondendo PING
// normalmente, e só o tamanho do conjunto de workers denuncia a parada.
func TestRedisCheckDownWhenAssertionFails(t *testing.T) {
	srv := startFakeRedis(t, func(cmd []string) string {
		switch cmd[0] {
		case "SCARD":
			return ":0\r\n"
		case "LLEN":
			return ":3\r\n"
		}
		return ""
	})
	cfg := `{"assertions":[
		{"command":"LLEN","key":"rq:queue:default","op":"lt","value":100},
		{"command":"SCARD","key":"rq:workers:default","op":"gte","value":1}]}`

	res := checker.NewRedis().Check(context.Background(), redisMonitor(srv.addr, cfg))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if want := "SCARD rq:workers:default = 0, esperado >= 1"; res.Message != want {
		t.Errorf("Message = %q, want %q", res.Message, want)
	}
}

func TestRedisCheckUpWhenAssertionsPass(t *testing.T) {
	srv := startFakeRedis(t, func(cmd []string) string {
		switch cmd[0] {
		case "SCARD":
			return ":2\r\n"
		case "GET":
			return "$1\r\n5\r\n"
		}
		return ""
	})
	cfg := `{"assertions":[
		{"command":"SCARD","key":"rq:workers:default","op":"gte","value":1},
		{"command":"get","key":"contador","op":"eq","value":5}]}`

	res := checker.NewRedis().Check(context.Background(), redisMonitor(srv.addr, cfg))

	if res.Status != domain.StatusUp {
		t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
	}

	var sent []string
	for _, c := range srv.commands() {
		sent = append(sent, strings.Join(c, " "))
	}
	joined := strings.Join(sent, "|")
	if !strings.Contains(joined, "SCARD rq:workers:default") || !strings.Contains(joined, "GET contador") {
		t.Errorf("commands sent = %v, want SCARD and GET", sent)
	}
}

// Com o limite de memória, um Redis perto de "OOM command not allowed"
// acusa antes de começar a recusar escrita.
func TestRedisCheckDownWhenMemoryAboveLimit(t *testing.T) {
	srv := startFakeRedis(t, func(cmd []string) string {
		if cmd[0] == "INFO" {
			body := "# Memory\r\nused_memory:4096\r\n"
			return fmt.Sprintf("$%d\r\n%s\r\n", len(body), body)
		}
		return ""
	})

	res := checker.NewRedis().Check(context.Background(),
		redisMonitor(srv.addr, `{"max_used_memory_bytes":1024}`))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if want := "INFO memory used_memory = 4096, esperado <= 1024"; res.Message != want {
		t.Errorf("Message = %q, want %q", res.Message, want)
	}
}

func TestRedisCheckSendsCredentials(t *testing.T) {
	srv := startFakeRedis(t, nil)

	res := checker.NewRedis().Check(context.Background(),
		redisMonitor(srv.addr, `{"username":"monitor","password":"s3nha-do-redis"}`))

	if res.Status != domain.StatusUp {
		t.Fatalf("Status = %v (%s), want up", res.Status, res.Message)
	}
	found := false
	for _, c := range srv.commands() {
		if c[0] == "AUTH" && strings.Join(c[1:], " ") == "monitor s3nha-do-redis" {
			found = true
		}
	}
	if !found {
		t.Errorf("AUTH monitor <senha> was not sent; got %v", srv.commands())
	}
}

// A mensagem de causa vai para o painel, para o alerta no Slack e para o
// histórico exportado — lugares lidos por quem não deveria ver a senha.
// O servidor aqui ecoa a senha no erro, como alguns proxies fazem, e ela
// precisa sair mascarada.
func TestRedisCheckNeverLeaksThePasswordInTheCause(t *testing.T) {
	const secret = "s3nha-que-nao-pode-vazar"
	srv := startFakeRedis(t, func(cmd []string) string {
		if cmd[0] == "AUTH" {
			return "-WRONGPASS senha recusada: " + cmd[len(cmd)-1] + "\r\n"
		}
		return ""
	})

	res := checker.NewRedis().Check(context.Background(),
		redisMonitor(srv.addr, `{"password":"`+secret+`"}`))

	if res.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want down", res.Status)
	}
	if strings.Contains(res.Message, secret) {
		t.Errorf("Message leaks the password: %q", res.Message)
	}
	if !strings.Contains(res.Message, "WRONGPASS") {
		t.Errorf("Message = %q, want the rest of the cause preserved", res.Message)
	}
}
