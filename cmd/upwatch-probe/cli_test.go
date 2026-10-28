package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
)

// Tokens fictícios, no formato do UpWatch.
const (
	token     = "token-da-verificacao-0001"
	hbToken   = "token-do-sinal-de-vida-01"
	pushBase  = "https://upwatch.exemplo.com/api/v1/push/"
	redisLine = "SCARD rq:workers:default >= 1"
)

type harness struct {
	t      *testing.T
	path   string
	stdin  string
	tty    bool
	env    map[string]string
	out    bytes.Buffer
	errOut bytes.Buffer
	// measure substitui a verificação real nos testes que não querem rede.
	measure func(context.Context, probe.Check) probe.Signal
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return &harness{t: t, path: filepath.Join(t.TempDir(), "probe.yaml"), env: map[string]string{}}
}

func (h *harness) run(args ...string) int {
	h.t.Helper()
	h.out.Reset()
	h.errOut.Reset()
	a := &app{
		stdin: strings.NewReader(h.stdin), stdout: &h.out, stderr: &h.errOut,
		getenv:  func(k string) string { return h.env[k] },
		tty:     h.tty,
		version: "teste",
		measure: h.measure,
	}
	return a.main(context.Background(), append(args, "--config", h.path))
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	if code := h.run(args...); code != 0 {
		h.t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, h.out.String(), h.errOut.String())
	}
	return h.out.String()
}

func (h *harness) config() probe.Config {
	h.t.Helper()
	cfg, err := probe.Load(h.path)
	if err != nil {
		h.t.Fatalf("Load: %v", err)
	}
	return cfg
}

// O caso que motivou a sonda, colado no terminal do jump server: tudo
// num comando de uma linha, sem JSON e sem aspas aninhadas.
func TestAddListRemoveOnOneLine(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "rq-workers", "--type", "redis", "--target", "redis:6379",
		"--assert", redisLine, "--assert", "LLEN rq:queue:default < 100",
		"--push-url", pushBase+token, "--interval", "60s")
	h.mustRun("add", "--name", "bff", "--type", "http", "--target", "http://bff-api:3000/health",
		"--push-url", pushBase+token, "--expect-status", "200")

	cfg := h.config()
	if len(cfg.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(cfg.Checks))
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(h.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("config mode = %o, want 600", info.Mode().Perm())
		}
	}

	list := h.mustRun("list")
	for _, want := range []string{"rq-workers", "redis:6379", redisLine, "LLEN rq:queue:default < 100", "bff", "http://bff-api:3000/health"} {
		if !strings.Contains(list, want) {
			t.Errorf("list misses %q:\n%s", want, list)
		}
	}
	if strings.Contains(list, token) {
		t.Errorf("list shows the push token:\n%s", list)
	}

	h.mustRun("remove", "rq-workers")
	if _, ok := func() (*probe.Check, bool) { c := h.config(); return c.Find("rq-workers") }(); ok {
		t.Error("remove kept the check")
	}
	if code := h.run("remove", "rq-workers"); code == 0 {
		t.Error("removing a missing check succeeded")
	}
}

// Um add com erro não grava nada e diz o que corrigir, na saída de erro.
func TestAddRejectsAWriteCommandAndKeepsTheFile(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "a", "--type", "tcp", "--target", "redis:6379", "--push-url", pushBase+token)
	before, _ := os.ReadFile(h.path)

	code := h.run("add", "--name", "fila", "--type", "redis", "--target", "redis:6379",
		"--assert", "FLUSHALL > 0", "--push-url", pushBase+token)
	if code == 0 {
		t.Fatal("add accepted a write command")
	}
	if !strings.Contains(h.errOut.String(), "FLUSHALL") {
		t.Errorf("stderr does not say what is wrong: %q", h.errOut.String())
	}
	after, _ := os.ReadFile(h.path)
	if !bytes.Equal(before, after) {
		t.Error("a rejected add changed the file")
	}
}

func TestAddRefusesADuplicateUnlessReplace(t *testing.T) {
	h := newHarness(t)
	args := []string{"add", "--name", "a", "--type", "tcp", "--target", "x:1", "--push-url", pushBase + token}
	h.mustRun(args...)
	if code := h.run(args...); code == 0 {
		t.Fatal("a second add with the same name succeeded")
	}
	h.mustRun(append(args[:len(args):len(args)], "--replace", "--interval", "30s")...)
	if c, _ := func() (*probe.Check, bool) { c := h.config(); return c.Find("a") }(); time.Duration(c.Interval) != 30*time.Second {
		t.Errorf("replace did not take: interval %s", c.Interval)
	}
}

// A senha nunca vai em flag: ficaria no histórico do terminal, na
// gravação do jump server e no ps de qualquer usuário da VM.
func TestAddReadsThePasswordFromStdin(t *testing.T) {
	h := newHarness(t)
	h.stdin = "s3nha-que-nao-pode-vazar\n"
	h.mustRun("add", "--name", "fila", "--type", "redis", "--target", "redis:6379",
		"--username", "upwatch", "--password-stdin", "--push-url", pushBase+token)

	c, _ := func() (*probe.Check, bool) { c := h.config(); return c.Find("fila") }()
	if c.Config["password"] != "s3nha-que-nao-pode-vazar" {
		t.Errorf("password = %v", c.Config["password"])
	}
	if out := h.mustRun("list"); strings.Contains(out, "s3nha") {
		t.Errorf("list shows the password:\n%s", out)
	}
}

func TestInitSetsTheProbeAndHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "--name", "vm-prod-1", "--heartbeat-url", pushBase+hbToken, "--heartbeat-interval", "30s")
	cfg := h.config()
	if cfg.Name != "vm-prod-1" || cfg.Heartbeat.PushURL != pushBase+hbToken || time.Duration(cfg.Heartbeat.Interval) != 30*time.Second {
		t.Errorf("config = %+v", cfg)
	}
	// O init gera o token da interface, para ela nunca subir aberta.
	if len(cfg.UIToken) < 32 {
		t.Errorf("ui token = %q, want a generated one", cfg.UIToken)
	}
}

func TestUITokenShowsAndRotates(t *testing.T) {
	h := newHarness(t)
	h.mustRun("init", "--name", "vm")
	first := strings.TrimSpace(h.mustRun("ui-token"))
	if first != h.config().UIToken {
		t.Fatalf("ui-token printed %q, config has %q", first, h.config().UIToken)
	}
	second := strings.TrimSpace(h.mustRun("ui-token", "--rotate"))
	if second == first || second != h.config().UIToken {
		t.Errorf("rotate printed %q (was %q), config has %q", second, first, h.config().UIToken)
	}
}

type pushRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (p *pushRecorder) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(raw))
		p.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *pushRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func addAgainst(h *harness, srvURL string) {
	h.mustRun("init", "--name", "vm", "--allow-http-push")
	h.mustRun("add", "--name", "fila", "--type", "redis", "--target", "redis:6379",
		"--assert", redisLine, "--push-url", srvURL+"/api/v1/push/"+token)
}

var downSignal = probe.Signal{Status: "down", Msg: "SCARD rq:workers:default = 0, esperado >= 1", LatencyMS: 3}

// --dry-run mostra o corpo byte a byte e não manda nada: é como alguém
// confere o que sai da rede antes de autorizar a sonda.
func TestTestDryRunPrintsTheExactBodyAndSendsNothing(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)
	h := newHarness(t)
	h.measure = func(context.Context, probe.Check) probe.Signal { return downSignal }
	addAgainst(h, srv.URL)

	out := h.mustRun("test", "fila", "--dry-run")
	if !strings.Contains(out, string(downSignal.Body())) {
		t.Errorf("dry-run does not print the exact body %s:\n%s", downSignal.Body(), out)
	}
	if strings.Contains(out, token) {
		t.Errorf("dry-run shows the push token:\n%s", out)
	}
	if rec.count() != 0 {
		t.Errorf("dry-run sent %d requests", rec.count())
	}
}

func TestTestSendsOnceAndShowsTheResult(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)
	h := newHarness(t)
	h.measure = func(context.Context, probe.Check) probe.Signal { return downSignal }
	addAgainst(h, srv.URL)

	out := h.mustRun("test", "fila")
	if rec.count() != 1 || rec.bodies[0] != string(downSignal.Body()) {
		t.Fatalf("sent %v, want exactly the body once", rec.bodies)
	}
	for _, want := range []string{"down", downSignal.Msg, "3ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if code := h.run("test", "nao-existe"); code == 0 {
		t.Error("test of a missing check succeeded")
	}
}

// O daemon em --dry-run imprime cada envio em vez de fazê-lo.
func TestRunDryRunPrintsInsteadOfSending(t *testing.T) {
	rec := &pushRecorder{}
	srv := rec.server(t)
	h := newHarness(t)
	h.measure = nil
	h.mustRun("init", "--name", "vm", "--allow-http-push", "--heartbeat-url", srv.URL+"/api/v1/push/"+hbToken)

	out := &syncWriter{}
	a := &app{stdin: strings.NewReader(""), stdout: out, stderr: io.Discard,
		getenv: func(string) string { return "" }, version: "teste"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() { done <- a.main(ctx, []string{"run", "--dry-run", "--config", h.path}) }()

	want := string(probe.Signal{Status: "up", Msg: "sonda vm viva"}.Body())
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("run --dry-run did not print %s:\n%s", want, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("run exited %d", code)
	}
	if rec.count() != 0 {
		t.Errorf("dry-run sent %d requests", rec.count())
	}
	if strings.Contains(out.String(), hbToken) {
		t.Errorf("dry-run shows the token:\n%s", out.String())
	}
}

type syncWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncWriter) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// O status lê o arquivo que o daemon grava: roda por docker exec, noutro
// processo.
func TestStatusShowsLastResultAndDelivery(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "fila", "--type", "tcp", "--target", "redis:6379", "--push-url", pushBase+token)

	st := probe.NewStateFile(probe.StatePath(h.path))
	st.RecordResult("fila", time.Now(), downSignal)
	st.RecordDelivery("fila", probe.Delivery{At: time.Now(), Attempts: 4, HTTPStatus: 401, Err: "o central recusou o token (401)", GaveUp: true})

	out := h.mustRun("status")
	for _, want := range []string{"fila", "down", downSignal.Msg, "401"} {
		if !strings.Contains(out, want) {
			t.Errorf("status misses %q:\n%s", want, out)
		}
	}
}

func TestStatusWithoutDaemonSaysSo(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "fila", "--type", "tcp", "--target", "redis:6379", "--push-url", pushBase+token)
	out := h.mustRun("status")
	if !strings.Contains(out, "daemon") {
		t.Errorf("status without state does not mention the daemon:\n%s", out)
	}
}

// Cor só em terminal de verdade e só sem NO_COLOR: o terminal web do jump
// server mostra o código de escape como lixo na tela.
func TestColorOnlyOnATerminalWithoutNoColor(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "fila", "--type", "tcp", "--target", "redis:6379", "--push-url", pushBase+token)
	st := probe.NewStateFile(probe.StatePath(h.path))
	st.RecordResult("fila", time.Now(), downSignal)

	h.tty = false
	if out := h.mustRun("status"); strings.Contains(out, "\x1b[") {
		t.Errorf("escape codes without a TTY:\n%q", out)
	}
	h.tty = true
	if out := h.mustRun("status"); !strings.Contains(out, "\x1b[") {
		t.Errorf("no color on a TTY:\n%q", out)
	}
	h.env["NO_COLOR"] = "1"
	if out := h.mustRun("status"); strings.Contains(out, "\x1b[") {
		t.Errorf("escape codes with NO_COLOR:\n%q", out)
	}
}

func TestListAsJSONForScripts(t *testing.T) {
	h := newHarness(t)
	h.mustRun("add", "--name", "fila", "--type", "tcp", "--target", "redis:6379", "--push-url", pushBase+token)
	var got []map[string]any
	if err := json.Unmarshal([]byte(h.mustRun("list", "--json")), &got); err != nil || len(got) != 1 {
		t.Fatalf("list --json = %v, %v", got, err)
	}
	if strings.Contains(h.out.String(), token) {
		t.Errorf("list --json shows the token")
	}
}
