package scheduler_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/scheduler"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// probe é um Checker instrumentado: registra cada chamada, mede a
// concorrência real e permite segurar a execução para simular lentidão.
type probe struct {
	calls chan int64 // id do monitor verificado

	inFlight atomic.Int32
	maxSeen  atomic.Int32

	hold     chan struct{} // se não-nil, o check bloqueia até liberar
	panicNow atomic.Bool

	mu       sync.Mutex
	timeouts []time.Duration
}

func newProbe() *probe {
	return &probe{calls: make(chan int64, 256)}
}

func (p *probe) Type() domain.MonitorType { return domain.MonitorHTTP }

func (p *probe) ValidateConfig(json.RawMessage) error { return nil }

func (p *probe) Check(ctx context.Context, m domain.Monitor) checker.Result {
	cur := p.inFlight.Add(1)
	for {
		peak := p.maxSeen.Load()
		if cur <= peak || p.maxSeen.CompareAndSwap(peak, cur) {
			break
		}
	}
	defer p.inFlight.Add(-1)

	if deadline, ok := ctx.Deadline(); ok {
		p.mu.Lock()
		p.timeouts = append(p.timeouts, time.Until(deadline).Round(time.Second))
		p.mu.Unlock()
	}

	p.calls <- m.ID

	if p.panicNow.Load() {
		panic("checker explodiu")
	}
	if p.hold != nil {
		<-p.hold
	}
	return checker.Result{Status: domain.StatusUp, LatencyMS: 12}
}

// awaitCall espera uma verificação acontecer, falhando se travar.
func awaitCall(t *testing.T, p *probe) int64 {
	t.Helper()
	select {
	case id := <-p.calls:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a check to run")
		return 0
	}
}

func expectNoCall(t *testing.T, p *probe, within time.Duration) {
	t.Helper()
	select {
	case id := <-p.calls:
		t.Fatalf("monitor %d was checked, want no check at all", id)
	case <-time.After(within):
	}
}

// sink captura as batidas produzidas pelo agendador.
type sink struct {
	mu   sync.Mutex
	got  []domain.Heartbeat
	sent chan struct{}
}

func newSink() *sink { return &sink{sent: make(chan struct{}, 256)} }

func (s *sink) Submit(_ context.Context, hb domain.Heartbeat) error {
	s.mu.Lock()
	s.got = append(s.got, hb)
	s.mu.Unlock()
	s.sent <- struct{}{}
	return nil
}

func (s *sink) all() []domain.Heartbeat {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.Heartbeat(nil), s.got...)
}

func awaitHeartbeat(t *testing.T, s *sink) {
	t.Helper()
	select {
	case <-s.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a heartbeat to be submitted")
	}
}

func monitor(id int64, interval time.Duration) domain.Monitor {
	return domain.Monitor{
		ID: id, Name: "alvo", Type: domain.MonitorHTTP, Target: "https://example.com",
		Interval: interval, Timeout: interval / 2, ConfirmationThreshold: 1,
		Enabled: true, Config: json.RawMessage("{}"),
	}
}

// newScheduler monta o agendador com relógio falso e jitter desligado,
// para os testes controlarem o tempo integralmente.
func newScheduler(t *testing.T, p *probe, s *sink, workers int) (*scheduler.Scheduler, *clock.Fake) {
	t.Helper()

	reg, err := checker.NewRegistry(p)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	fake := clock.NewFake(epoch)
	sch := scheduler.New(reg, s, scheduler.Options{
		Workers:       workers,
		Clock:         fake,
		DisableJitter: true,
	})
	return sch, fake
}

// start sobe o laço e devolve o cancelamento; a limpeza garante o retorno.
func start(t *testing.T, s *scheduler.Scheduler) (context.CancelFunc, <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return cancel, done
}

func TestSchedulerChecksMonitorImmediatelyOnStart(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	if got := awaitCall(t, p); got != 1 {
		t.Errorf("checked monitor %d, want 1", got)
	}
}

func TestSchedulerRepeatsAtConfiguredInterval(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p) // execução imediata
	awaitHeartbeat(t, sk)

	// Espera o laço rearmar para o próximo vencimento antes de mexer no
	// relógio. Contar timers não bastaria: o laço mantém sempre um armado,
	// então BlockUntil(1) já estaria satisfeito pelo timer anterior.
	fake.BlockUntilDeadline(epoch.Add(time.Minute))
	fake.Advance(time.Minute)

	if got := awaitCall(t, p); got != 1 {
		t.Errorf("checked monitor %d, want 1", got)
	}
}

func TestSchedulerDoesNotRunBeforeTheIntervalElapses(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)

	fake.BlockUntilDeadline(epoch.Add(time.Minute))
	fake.Advance(59 * time.Second)

	expectNoCall(t, p, 200*time.Millisecond)
}

func TestSchedulerRunsMonitorsWithDifferentIntervals(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	sch.Upsert(monitor(2, 5*time.Minute))
	start(t, sch)

	// Ambos vencem na largada.
	seen := map[int64]bool{awaitCall(t, p): true}
	seen[awaitCall(t, p)] = true
	if !seen[1] || !seen[2] {
		t.Fatalf("initial run covered %v, want both monitors", seen)
	}
	awaitHeartbeat(t, sk)
	awaitHeartbeat(t, sk)

	fake.BlockUntilDeadline(epoch.Add(time.Minute))
	fake.Advance(time.Minute)

	// Só o de um minuto venceu.
	if got := awaitCall(t, p); got != 1 {
		t.Errorf("checked monitor %d, want only monitor 1 to be due", got)
	}
	expectNoCall(t, p, 200*time.Millisecond)
}

// Sem teto de concorrência, mil monitores disparam juntos e estouram os
// descritores de arquivo do processo.
func TestSchedulerNeverExceedsWorkerLimit(t *testing.T) {
	p, sk := newProbe(), newSink()
	p.hold = make(chan struct{})
	sch, _ := newScheduler(t, p, sk, 3)

	for i := int64(1); i <= 20; i++ {
		sch.Upsert(monitor(i, time.Minute))
	}
	start(t, sch)

	// Deixa três checks entrarem e ficarem presos.
	for i := 0; i < 3; i++ {
		awaitCall(t, p)
	}
	// Nenhum quarto pode começar enquanto os três seguram o pool.
	expectNoCall(t, p, 300*time.Millisecond)

	if got := p.maxSeen.Load(); got > 3 {
		t.Errorf("observed %d concurrent checks, want at most 3", got)
	}
	close(p.hold)
}

func TestSchedulerSkipsDisabledMonitors(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)
	m := monitor(1, time.Minute)
	m.Enabled = false
	sch.Upsert(m)
	start(t, sch)

	expectNoCall(t, p, 300*time.Millisecond)
}

func TestSchedulerPicksUpMonitorAddedWhileRunning(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)
	start(t, sch)

	sch.Upsert(monitor(7, time.Minute))

	if got := awaitCall(t, p); got != 7 {
		t.Errorf("checked monitor %d, want 7", got)
	}
}

func TestSchedulerStopsCheckingRemovedMonitor(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)
	fake.BlockUntilDeadline(epoch.Add(time.Minute))

	sch.Remove(1)
	fake.Advance(5 * time.Minute)

	expectNoCall(t, p, 300*time.Millisecond)
}

// Pausar precisa ter efeito imediato: continuar sondando um alvo em
// manutenção gera alerta que o operador já sabe que é falso.
func TestSchedulerStopsCheckingDisabledMonitor(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)
	fake.BlockUntilDeadline(epoch.Add(time.Minute))

	paused := monitor(1, time.Minute)
	paused.Enabled = false
	sch.Upsert(paused)

	fake.Advance(5 * time.Minute)

	expectNoCall(t, p, 300*time.Millisecond)
}

func TestSchedulerRescheduleWhenIntervalChanges(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Hour))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)
	fake.BlockUntilDeadline(epoch.Add(time.Hour))

	sch.Upsert(monitor(1, 10*time.Second))
	fake.BlockUntilDeadline(epoch.Add(10 * time.Second))
	fake.Advance(10 * time.Second)

	if got := awaitCall(t, p); got != 1 {
		t.Errorf("checked monitor %d, want 1", got)
	}
}

// Um check mais lento que o próprio intervalo não pode acumular execuções:
// o alvo lento receberia uma avalanche de requisições justamente quando
// está sofrendo.
func TestSchedulerDoesNotStackSlowChecks(t *testing.T) {
	p, sk := newProbe(), newSink()
	p.hold = make(chan struct{})
	sch, fake := newScheduler(t, p, sk, 8)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p) // preso no hold

	fake.Advance(10 * time.Minute) // dez intervalos passam

	expectNoCall(t, p, 300*time.Millisecond)
	close(p.hold)
}

func TestSchedulerSubmitsHeartbeatForEachCheck(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(42, time.Minute))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)

	got := sk.all()
	if len(got) != 1 {
		t.Fatalf("submitted %d heartbeats, want 1", len(got))
	}
	if got[0].MonitorID != 42 {
		t.Errorf("MonitorID = %d, want 42", got[0].MonitorID)
	}
	if got[0].Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", got[0].Status, domain.StatusUp)
	}
	if got[0].LatencyMS != 12 {
		t.Errorf("LatencyMS = %d, want 12", got[0].LatencyMS)
	}
	if got[0].Timestamp.IsZero() {
		t.Error("Timestamp is zero, want the moment of the check")
	}
}

// O timeout configurado precisa chegar ao checker; sem ele um alvo que
// nunca responde prenderia um worker para sempre.
func TestSchedulerAppliesMonitorTimeoutToCheckContext(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)
	m := monitor(1, time.Minute)
	m.Timeout = 30 * time.Second
	sch.Upsert(m)
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.timeouts) == 0 {
		t.Fatal("check context carried no deadline, want the monitor timeout")
	}
	if p.timeouts[0] != 30*time.Second {
		t.Errorf("deadline in %v, want %v", p.timeouts[0], 30*time.Second)
	}
}

// Um checker defeituoso não pode parar o monitoramento dos demais alvos.
func TestSchedulerSurvivesPanickingChecker(t *testing.T) {
	p, sk := newProbe(), newSink()
	p.panicNow.Store(true)
	sch, fake := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	start(t, sch)

	awaitCall(t, p)
	awaitHeartbeat(t, sk)

	got := sk.all()
	if len(got) != 1 || got[0].Status != domain.StatusDown {
		t.Fatalf("heartbeats = %v, want one Down heartbeat from the panic", got)
	}

	// O laço continua vivo e agenda a próxima rodada.
	p.panicNow.Store(false)
	fake.BlockUntilDeadline(epoch.Add(time.Minute))
	fake.Advance(time.Minute)

	if id := awaitCall(t, p); id != 1 {
		t.Errorf("checked monitor %d after the panic, want 1", id)
	}
}

// Encerrar no meio de um check perderia a medição já iniciada.
func TestSchedulerWaitsForInFlightChecksOnShutdown(t *testing.T) {
	p, sk := newProbe(), newSink()
	p.hold = make(chan struct{})
	sch, _ := newScheduler(t, p, sk, 4)
	sch.Upsert(monitor(1, time.Minute))
	cancel, done := start(t, sch)

	awaitCall(t, p)
	cancel()

	select {
	case <-done:
		t.Fatal("Run returned while a check was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	close(p.hold)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the in-flight check finished")
	}

	if len(sk.all()) != 1 {
		t.Errorf("submitted %d heartbeats, want the in-flight check to be recorded", len(sk.all()))
	}
}

// Jitter existe para que cem monitores de mesmo intervalo não disparem no
// mesmo instante e produzam picos periódicos de carga.
func TestSchedulerSpreadsFirstRunWithJitter(t *testing.T) {
	p, sk := newProbe(), newSink()
	reg, err := checker.NewRegistry(p)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	fake := clock.NewFake(epoch)
	sch := scheduler.New(reg, sk, scheduler.Options{Workers: 8, Clock: fake})

	for i := int64(1); i <= 10; i++ {
		sch.Upsert(monitor(i, time.Minute))
	}
	start(t, sch)

	// Com jitter, nada vence no instante zero.
	expectNoCall(t, p, 200*time.Millisecond)

	fake.Advance(time.Minute)

	seen := map[int64]bool{}
	for i := 0; i < 10; i++ {
		seen[awaitCall(t, p)] = true
	}
	if len(seen) != 10 {
		t.Errorf("first cycle covered %d monitors, want all 10", len(seen))
	}
}

// O deslocamento vem do id, não de sorteio: precisa repetir entre
// reinícios para o comportamento ser reproduzível.
func TestSchedulerJitterIsDeterministic(t *testing.T) {
	nextRuns := func() []time.Time {
		p, sk := newProbe(), newSink()
		reg, _ := checker.NewRegistry(p)
		sch := scheduler.New(reg, sk, scheduler.Options{Workers: 4, Clock: clock.NewFake(epoch)})

		var out []time.Time
		for i := int64(1); i <= 5; i++ {
			sch.Upsert(monitor(i, time.Minute))
			at, ok := sch.NextRun(i)
			if !ok {
				t.Fatalf("monitor %d was not scheduled", i)
			}
			out = append(out, at)
		}
		return out
	}

	first, second := nextRuns(), nextRuns()

	for i := range first {
		if !first[i].Equal(second[i]) {
			t.Errorf("monitor %d scheduled at %v then %v: jitter is not deterministic",
				i+1, first[i], second[i])
		}
	}
	// E de fato espalha, em vez de colocar todos no mesmo instante.
	if first[0].Equal(first[4]) {
		t.Error("all monitors landed on the same instant, want the jitter to spread them")
	}
}

func TestSchedulerAppliesDefaultWorkerCount(t *testing.T) {
	p, sk := newProbe(), newSink()
	reg, _ := checker.NewRegistry(p)
	sch := scheduler.New(reg, sk, scheduler.Options{})

	if sch.Workers() != scheduler.DefaultWorkers {
		t.Errorf("Workers() = %d, want %d", sch.Workers(), scheduler.DefaultWorkers)
	}
}

func TestSchedulerCountsScheduledMonitors(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)

	sch.Upsert(monitor(1, time.Minute))
	sch.Upsert(monitor(2, time.Minute))
	sch.Upsert(monitor(1, time.Minute)) // mesmo id: atualiza, não duplica

	if got := sch.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}

	sch.Remove(1)
	if got := sch.Len(); got != 1 {
		t.Errorf("Len() after Remove = %d, want 1", got)
	}
}

func TestSchedulerNextRunReportsUnknownMonitor(t *testing.T) {
	p, sk := newProbe(), newSink()
	sch, _ := newScheduler(t, p, sk, 4)

	if _, ok := sch.NextRun(999); ok {
		t.Error("NextRun of an unscheduled monitor reported ok, want false")
	}
}
