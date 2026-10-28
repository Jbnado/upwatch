package probe_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/probe"
)

// O status roda noutro processo (docker exec) e só enxerga o daemon pelo
// arquivo de estado. Ele fica no volume, ao lado da configuração, porque
// é o único lugar gravável com o contêiner em --read-only.
func TestStateFileIsReadableByAnotherProcess(t *testing.T) {
	dir := t.TempDir()
	path := probe.StatePath(filepath.Join(dir, "probe.yaml"))
	if filepath.Dir(path) != dir {
		t.Fatalf("state path %q is not beside the config", path)
	}

	st := probe.NewStateFile(path)
	st.RecordResult("rq-workers", epoch, probe.Signal{Status: "down", Msg: "SCARD rq:workers:default = 0, esperado >= 1", LatencyMS: 3})
	st.RecordDelivery("rq-workers", probe.Delivery{At: epoch.Add(time.Second), OK: false, Attempts: 4, HTTPStatus: 503, Err: "o central respondeu 503", GaveUp: true})
	st.RecordResult("velha", epoch, probe.Signal{Status: "up"})
	st.Keep([]string{"rq-workers"})

	got, err := probe.ReadState(path)
	if err != nil {
		t.Fatalf("ReadState returned unexpected error: %v", err)
	}
	c, ok := got.Checks["rq-workers"]
	if !ok || c.Result == nil || c.Delivery == nil {
		t.Fatalf("state = %+v", got)
	}
	if c.Result.Status != "down" || c.Result.LatencyMS != 3 || !strings.Contains(c.Result.Msg, "SCARD") {
		t.Errorf("result = %+v", c.Result)
	}
	if c.Delivery.HTTPStatus != 503 || !c.Delivery.GaveUp || c.Delivery.Attempts != 4 {
		t.Errorf("delivery = %+v", c.Delivery)
	}
	if _, ok := got.Checks["velha"]; ok {
		t.Error("a removed check is still in the state")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("state mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func tcpConfig(addr string, names ...string) string {
	var b strings.Builder
	b.WriteString("checks:\n")
	for _, n := range names {
		b.WriteString("  - name: " + n + "\n    type: tcp\n    target: " + addr +
			"\n    interval: 1m\n    timeout: 2s\n    push_url: " + checkURL + "\n")
	}
	return b.String()
}

type daemonFixture struct {
	path   string
	clk    *clock.Fake
	daemon *probe.Daemon
	hup    chan os.Signal
	logs   *syncBuffer
	addr   string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func startDaemon(t *testing.T, initial string) *daemonFixture {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })

	f := &daemonFixture{
		path: filepath.Join(t.TempDir(), "probe.yaml"),
		clk:  clock.NewFake(epoch),
		hup:  make(chan os.Signal, 1),
		logs: &syncBuffer{},
		addr: l.Addr().String(),
	}
	if initial != "" {
		if err := os.WriteFile(f.path, []byte(strings.ReplaceAll(initial, "ADDR", f.addr)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := newScriptedSend()
	f.daemon = probe.NewDaemon(probe.DaemonOptions{
		Path: f.path, Clock: f.clk, Send: s.send, Backoff: testBackoff,
		Logger:   slog.New(slog.NewTextHandler(f.logs, nil)),
		Registry: probe.NewRegistry(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = f.daemon.Run(ctx, f.hup); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return f
}

func (f *daemonFixture) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(f.path, []byte(strings.ReplaceAll(content, "ADDR", f.addr)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tick espera o watcher armar a próxima conferência e a dispara.
func (f *daemonFixture) tick() {
	f.clk.BlockUntilDeadline(f.clk.Now().Add(probe.WatchInterval))
	f.clk.Advance(probe.WatchInterval)
}

func (f *daemonFixture) has(name string) bool {
	_, ok := f.daemon.State().Checks[name]
	return ok
}

// O add roda por docker exec e só mexe no arquivo. O daemon tem de
// perceber sozinho, sem reinício: reiniciar o contêiner pelo terminal do
// jump server é justamente o que não se quer pedir a ninguém.
func TestDaemonPicksUpAChangedFile(t *testing.T) {
	f := startDaemon(t, tcpConfig("ADDR", "a"))
	waitFor(t, func() bool { return f.has("a") })

	f.write(t, tcpConfig("ADDR", "a", "b"))
	f.tick()
	waitFor(t, func() bool { return f.has("b") })
}

// Sem configuração ainda (contêiner recém-criado, antes do primeiro add),
// o daemon espera em vez de sair: um contêiner com --restart reiniciando
// em laço encheria o log e não ajudaria quem está configurando.
func TestDaemonWaitsForTheFirstConfig(t *testing.T) {
	f := startDaemon(t, "")
	waitFor(t, func() bool { return strings.Contains(f.logs.String(), "aguardando") })

	f.write(t, tcpConfig("ADDR", "a"))
	f.tick()
	waitFor(t, func() bool { return f.has("a") })
}

// Uma edição com erro não derruba o que estava rodando: a sonda segue
// com a última configuração boa e diz no log o que está errado.
func TestDaemonKeepsTheLastGoodConfigOnABrokenEdit(t *testing.T) {
	f := startDaemon(t, tcpConfig("ADDR", "a"))
	waitFor(t, func() bool { return f.has("a") })

	f.write(t, "checks:\n  - name: a\n    intervall: 1m\n")
	f.tick()
	waitFor(t, func() bool { return strings.Contains(f.logs.String(), "intervall") })

	if cfg := f.daemon.Config(); len(cfg.Checks) != 1 || cfg.Checks[0].Name != "a" {
		t.Errorf("config after a broken edit = %+v, want the last good one", cfg)
	}
}

// SIGHUP relê na hora, sem esperar a próxima conferência do arquivo.
func TestDaemonReloadsOnHangup(t *testing.T) {
	f := startDaemon(t, tcpConfig("ADDR", "a"))
	waitFor(t, func() bool { return f.has("a") })

	f.write(t, tcpConfig("ADDR", "a", "c"))
	f.hup <- syscall.SIGHUP
	waitFor(t, func() bool { return f.has("c") })
}

// O token não aparece no log, nem quando a entrega falha.
func TestDaemonLogNeverShowsThePushToken(t *testing.T) {
	f := startDaemon(t, tcpConfig("ADDR", "a"))
	waitFor(t, func() bool { return f.has("a") })
	f.write(t, tcpConfig("ADDR", "a", "b"))
	f.tick()
	waitFor(t, func() bool { return f.has("b") })

	if strings.Contains(f.logs.String(), "token-da-verificacao-0001") {
		t.Errorf("log shows the push token:\n%s", f.logs.String())
	}
}
