// Package integration_test exercita a cadeia completa do M1 com
// componentes reais: agendador, checker HTTP, batch writer e SQLite.
//
// Os testes de unidade provam cada peça isoladamente; este prova que elas
// se encaixam — que um alvo HTTP de verdade vira uma linha no banco.
package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/scheduler"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

// pipeline é a cadeia montada, com os componentes já em execução.
type pipeline struct {
	store *sqlstore.Store
	sched *scheduler.Scheduler
	stop  context.CancelFunc
	done  *sync.WaitGroup
}

// newPipeline monta agendador, writer e store reais e os coloca a rodar.
func newPipeline(t *testing.T) *pipeline {
	t.Helper()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}

	writer := store.NewBatchWriter(st, store.BatchWriterOptions{
		MaxBatch: 10,
		Interval: 20 * time.Millisecond,
		Clock:    clock.Real(),
	})

	reg, err := checker.NewRegistry(checker.NewHTTP())
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}

	sched := scheduler.New(reg, writer, scheduler.Options{
		Workers:       4,
		Clock:         clock.Real(),
		DisableJitter: true, // primeiro check imediato, sem esperar o espalhamento
	})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); writer.Run(ctx) }()
	go func() { defer wg.Done(); sched.Run(ctx) }()

	p := &pipeline{store: st, sched: sched, stop: cancel, done: &wg}
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		if err := st.Close(); err != nil {
			t.Errorf("Close returned unexpected error: %v", err)
		}
	})
	return p
}

// addMonitor persiste o monitor e o entrega ao agendador, como fará a API.
func (p *pipeline) addMonitor(t *testing.T, name, target string, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage("{}")
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}

	m := domain.Monitor{
		Name: name, Type: domain.MonitorHTTP, Target: target,
		Interval: time.Minute, Timeout: 5 * time.Second,
		ConfirmationThreshold: 1, Enabled: true, Config: raw,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate returned unexpected error: %v", err)
	}
	if err := p.store.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	p.sched.Upsert(m)
	return m
}

// awaitHeartbeats espera n batidas do monitor aparecerem no banco.
func (p *pipeline) awaitHeartbeats(t *testing.T, monitorID int64, n int) []domain.Heartbeat {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, err := p.store.QueryHeartbeats(context.Background(), store.HeartbeatQuery{
			MonitorID: monitorID,
			Range: store.TimeRange{
				From: time.Now().Add(-time.Hour),
				To:   time.Now().Add(time.Hour),
			},
		})
		if err != nil {
			t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
		}
		if len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %d heartbeats from monitor %d", n, monitorID)
	return nil
}

// Prova central do M1: um alvo HTTP real vira batida persistida.
func TestPipelineRecordsHealthyTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	p := newPipeline(t)
	m := p.addMonitor(t, "alvo saudável", srv.URL, nil)

	got := p.awaitHeartbeats(t, m.ID, 1)

	if got[0].Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got[0].Status, got[0].Message, domain.StatusUp)
	}
	if got[0].ProbeID != domain.DefaultProbeID {
		t.Errorf("ProbeID = %q, want %q", got[0].ProbeID, domain.DefaultProbeID)
	}
	if got[0].Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp location = %v, want UTC", got[0].Timestamp.Location())
	}
}

func TestPipelineRecordsFailingTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	p := newPipeline(t)
	m := p.addMonitor(t, "alvo com erro", srv.URL, nil)

	got := p.awaitHeartbeats(t, m.ID, 1)

	if got[0].Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got[0].Status, domain.StatusDown)
	}
	if got[0].Message == "" {
		t.Error("Message is empty, want the cause of the failure")
	}
	// Batida sem resposta útil não guarda latência: o valor não
	// corresponderia a nenhum tempo de serviço observado.
	if got[0].LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0 for a failed check", got[0].LatencyMS)
	}
}

// A condição sobre o corpo precisa valer na cadeia inteira, não só no
// teste isolado do checker.
func TestPipelineHonoursBodyCondition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"degraded"}`))
	}))
	t.Cleanup(srv.Close)

	p := newPipeline(t)
	m := p.addMonitor(t, "alvo com corpo inesperado", srv.URL,
		map[string]any{"body_contains": "healthy"})

	got := p.awaitHeartbeats(t, m.ID, 1)

	if got[0].Status != domain.StatusDown {
		t.Errorf("Status = %v (%s), want %v", got[0].Status, got[0].Message, domain.StatusDown)
	}
}

// Vários monitores compartilham o mesmo pool e o mesmo writer; cada um
// precisa manter seu próprio histórico.
func TestPipelineKeepsMonitorsIndependent(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)

	p := newPipeline(t)
	healthy := p.addMonitor(t, "saudável", up.URL, nil)
	broken := p.addMonitor(t, "quebrado", down.URL, nil)

	healthyBeats := p.awaitHeartbeats(t, healthy.ID, 1)
	brokenBeats := p.awaitHeartbeats(t, broken.ID, 1)

	if healthyBeats[0].Status != domain.StatusUp {
		t.Errorf("healthy monitor status = %v, want %v", healthyBeats[0].Status, domain.StatusUp)
	}
	if brokenBeats[0].Status != domain.StatusDown {
		t.Errorf("broken monitor status = %v, want %v", brokenBeats[0].Status, domain.StatusDown)
	}
	for _, hb := range healthyBeats {
		if hb.MonitorID != healthy.ID {
			t.Errorf("heartbeat of monitor %d leaked into monitor %d's history", hb.MonitorID, healthy.ID)
		}
	}
}

// O alvo cai entre um check e o seguinte: é o caso que o produto existe
// para detectar.
func TestPipelineDetectsTargetGoingDown(t *testing.T) {
	var healthy = true
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		ok := healthy
		mu.Unlock()
		if ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	p := newPipeline(t)
	m := domain.Monitor{
		Name: "alvo instável", Type: domain.MonitorHTTP, Target: srv.URL,
		Interval: 50 * time.Millisecond, Timeout: 40 * time.Millisecond,
		ConfirmationThreshold: 1, Enabled: true, Config: json.RawMessage("{}"),
	}
	if err := p.store.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	p.sched.Upsert(m)

	p.awaitHeartbeats(t, m.ID, 1)

	mu.Lock()
	healthy = false
	mu.Unlock()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		beats := p.awaitHeartbeats(t, m.ID, 1)
		last := beats[len(beats)-1]
		if last.Status == domain.StatusDown {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("target went down but no Down heartbeat was ever recorded")
}

// O flush de desligamento precisa levar ao banco o que ainda estava em
// memória: perder essa janela apagaria justamente o momento da queda.
func TestPipelinePersistsPendingWorkOnShutdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer st.Close()

	// Intervalo de flush longo: nada chega ao banco antes do desligamento,
	// então o que for encontrado depois veio do flush final.
	writer := store.NewBatchWriter(st, store.BatchWriterOptions{
		MaxBatch: 1000, Interval: time.Hour, Clock: clock.Real(),
	})
	reg, _ := checker.NewRegistry(checker.NewHTTP())
	sched := scheduler.New(reg, writer, scheduler.Options{
		Workers: 4, Clock: clock.Real(), DisableJitter: true,
	})

	m := domain.Monitor{
		Name: "alvo", Type: domain.MonitorHTTP, Target: srv.URL,
		Interval: time.Minute, Timeout: 5 * time.Second,
		ConfirmationThreshold: 1, Enabled: true, Config: json.RawMessage("{}"),
	}
	if err := st.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); writer.Run(ctx) }()
	go func() { defer wg.Done(); sched.Run(ctx) }()
	sched.Upsert(m)

	// Espera o writer receber a batida sem que ela tenha ido ao banco.
	deadline := time.Now().Add(5 * time.Second)
	for writer.Pending() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if writer.Pending() == 0 {
		t.Fatal("no heartbeat reached the writer buffer")
	}

	cancel()
	wg.Wait()

	got, err := st.QueryHeartbeats(context.Background(), store.HeartbeatQuery{
		MonitorID: m.ID,
		Range:     store.TimeRange{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no heartbeat survived the shutdown, want the pending buffer to be flushed")
	}
}
