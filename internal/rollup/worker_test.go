package rollup_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/rollup"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

// now é o instante de referência: 14:30 UTC, dentro do bucket das 14h, de
// modo que a hora corrente esteja aberta e as anteriores fechadas.
var now = time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC)

// fixture monta um store real com relógio falso e o worker sob teste.
type fixture struct {
	store  *sqlstore.Store
	clock  *clock.Fake
	worker *rollup.Worker
}

func newFixture(t *testing.T, ret rollup.Retention) *fixture {
	t.Helper()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close returned unexpected error: %v", err)
		}
	})

	fake := clock.NewFake(now)
	return &fixture{
		store: st,
		clock: fake,
		worker: rollup.NewWorker(st, rollup.Options{
			Clock:     fake,
			Retention: ret,
		}),
	}
}

// defaultRetention mantém tudo dentro da janela, para os testes que não
// estão exercitando a poda.
func defaultRetention() rollup.Retention {
	return rollup.Retention{
		Raw:    7 * 24 * time.Hour,
		Hourly: 90 * 24 * time.Hour,
		Daily:  365 * 24 * time.Hour,
	}
}

func (f *fixture) monitor(t *testing.T, name string) int64 {
	t.Helper()

	m := domain.Monitor{
		Name: name, Type: domain.MonitorHTTP, Target: "https://example.com",
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: 1, Enabled: true,
	}
	if err := f.store.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return m.ID
}

// seed grava batidas a partir de at, uma por minuto.
func (f *fixture) seed(t *testing.T, monitorID int64, at time.Time, n int, status domain.Status, latency int64) {
	t.Helper()

	batch := make([]domain.Heartbeat, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, domain.Heartbeat{
			MonitorID: monitorID,
			Timestamp: at.Add(time.Duration(i) * time.Minute),
			Status:    status,
			LatencyMS: latency,
		})
	}
	if err := f.store.WriteHeartbeats(context.Background(), batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}
}

func (f *fixture) rollups(t *testing.T, monitorID int64, res domain.Resolution) []domain.Rollup {
	t.Helper()

	got, err := f.store.QueryRollups(context.Background(), store.RollupQuery{
		MonitorID:  monitorID,
		Resolution: res,
		Range:      store.TimeRange{From: now.Add(-400 * 24 * time.Hour), To: now.Add(24 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("QueryRollups returned unexpected error: %v", err)
	}
	return got
}

func (f *fixture) heartbeatCount(t *testing.T, monitorID int64) int {
	t.Helper()

	n := 0
	err := f.store.StreamHeartbeats(context.Background(), monitorID,
		store.TimeRange{From: now.Add(-400 * 24 * time.Hour), To: now.Add(24 * time.Hour)},
		func(domain.Heartbeat) error { n++; return nil })
	if err != nil {
		t.Fatalf("StreamHeartbeats returned unexpected error: %v", err)
	}
	return n
}

func (f *fixture) runCycle(t *testing.T) {
	t.Helper()
	if err := f.worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned unexpected error: %v", err)
	}
}

// hourAt devolve o início da hora deslocada em h a partir de now.
func hourAt(h int) time.Time {
	return now.Truncate(time.Hour).Add(time.Duration(h) * time.Hour)
}

func TestWorkerAggregatesClosedHourlyBucket(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	// 60 batidas cobrindo a hora das 13h, já encerrada.
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100)

	f.runCycle(t)

	got := f.rollups(t, id, domain.ResolutionHourly)
	if len(got) != 1 {
		t.Fatalf("produced %d hourly rollups, want 1", len(got))
	}
	if got[0].Total != 60 {
		t.Errorf("Total = %d, want 60", got[0].Total)
	}
	if got[0].UptimePercent() != 100 {
		t.Errorf("UptimePercent() = %v, want 100", got[0].UptimePercent())
	}
	if !got[0].BucketStart.Equal(hourAt(-1)) {
		t.Errorf("BucketStart = %v, want %v", got[0].BucketStart, hourAt(-1))
	}
}

// Agregar a hora corrente gravaria estatística parcial como se fosse
// definitiva, e ela nunca seria corrigida.
func TestWorkerLeavesTheOpenBucketAlone(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	// Batidas na hora das 14h, que ainda não terminou (agora são 14:30).
	f.seed(t, id, hourAt(0), 20, domain.StatusUp, 100)

	f.runCycle(t)

	if got := f.rollups(t, id, domain.ResolutionHourly); len(got) != 0 {
		t.Errorf("produced %d rollups for the open bucket, want 0", len(got))
	}
}

// O bucket diário sai das batidas cruas, não da média das horas: p95 de
// p95 não corresponde a nenhuma medição real.
func TestWorkerProducesHourlyAndDailyFromRawSamples(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")

	// Um dia inteiro já encerrado: 24 horas com uma batida por minuto.
	dayStart := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	for h := 0; h < 24; h++ {
		f.seed(t, id, dayStart.Add(time.Duration(h)*time.Hour), 60, domain.StatusUp, int64(100+h))
	}

	f.runCycle(t)

	hourly := f.rollups(t, id, domain.ResolutionHourly)
	if len(hourly) != 24 {
		t.Fatalf("produced %d hourly rollups, want 24", len(hourly))
	}

	daily := f.rollups(t, id, domain.ResolutionDaily)
	if len(daily) != 1 {
		t.Fatalf("produced %d daily rollups, want 1", len(daily))
	}
	if daily[0].Total != 24*60 {
		t.Errorf("daily Total = %d, want %d", daily[0].Total, 24*60)
	}
	// Latências vão de 100 a 123, uma faixa por hora. O máximo diário
	// precisa vir do dado cru.
	if daily[0].LatencyMaxMS != 123 {
		t.Errorf("daily LatencyMaxMS = %v, want 123", daily[0].LatencyMaxMS)
	}
	if daily[0].LatencyMinMS != 100 {
		t.Errorf("daily LatencyMinMS = %v, want 100", daily[0].LatencyMinMS)
	}
}

func TestWorkerAdvancesWatermark(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100)

	f.runCycle(t)

	mark, err := f.store.RollupWatermark(context.Background(), domain.ResolutionHourly)
	if err != nil {
		t.Fatalf("RollupWatermark returned unexpected error: %v", err)
	}
	if mark.Before(hourAt(-1)) {
		t.Errorf("watermark = %v, want it to have passed %v", mark, hourAt(-1))
	}
}

// Reprocessar precisa sobrescrever, não somar: uma reexecução após falha
// inflaria os contadores e mentiria sobre a disponibilidade.
func TestWorkerIsIdempotent(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100)

	f.runCycle(t)
	first := f.rollups(t, id, domain.ResolutionHourly)

	// Zera a marca d'água para forçar o reprocessamento do mesmo período.
	if err := f.store.SetRollupWatermark(context.Background(), domain.ResolutionHourly, time.Time{}); err != nil {
		t.Fatalf("SetRollupWatermark returned unexpected error: %v", err)
	}
	f.runCycle(t)
	second := f.rollups(t, id, domain.ResolutionHourly)

	if len(second) != len(first) {
		t.Fatalf("reprocessing produced %d rollups, want %d", len(second), len(first))
	}
	if second[0].Total != first[0].Total {
		t.Errorf("Total = %d after reprocessing, want %d", second[0].Total, first[0].Total)
	}
}

// Segunda passada sem dado novo não pode refazer trabalho já concluído.
func TestWorkerSkipsAlreadyProcessedBuckets(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100)

	f.runCycle(t)
	markAfterFirst, _ := f.store.RollupWatermark(context.Background(), domain.ResolutionHourly)

	f.runCycle(t)
	markAfterSecond, _ := f.store.RollupWatermark(context.Background(), domain.ResolutionHourly)

	if !markAfterFirst.Equal(markAfterSecond) {
		t.Errorf("watermark moved from %v to %v with no new data", markAfterFirst, markAfterSecond)
	}
	if got := f.rollups(t, id, domain.ResolutionHourly); len(got) != 1 {
		t.Errorf("produced %d rollups after a second pass, want 1", len(got))
	}
}

// Depois de o processo ficar horas parado, todas as janelas pendentes
// precisam ser agregadas — não só a mais recente.
func TestWorkerCatchesUpAfterAGap(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")

	for h := -5; h <= -1; h++ {
		f.seed(t, id, hourAt(h), 60, domain.StatusUp, 100)
	}

	f.runCycle(t)

	got := f.rollups(t, id, domain.ResolutionHourly)
	if len(got) != 5 {
		t.Fatalf("produced %d hourly rollups after the gap, want 5", len(got))
	}
}

// Monitor pausado ou recém-criado não gera linha de agregado vazia, que
// só inflaria a tabela e apareceria como zero por cento de uptime.
func TestWorkerSkipsBucketsWithoutSamples(t *testing.T) {
	f := newFixture(t, defaultRetention())
	withData := f.monitor(t, "com dados")
	idle := f.monitor(t, "sem dados")
	f.seed(t, withData, hourAt(-1), 60, domain.StatusUp, 100)

	f.runCycle(t)

	if got := f.rollups(t, idle, domain.ResolutionHourly); len(got) != 0 {
		t.Errorf("produced %d rollups for a monitor with no samples, want 0", len(got))
	}
}

func TestWorkerAggregatesEachMonitorSeparately(t *testing.T) {
	f := newFixture(t, defaultRetention())
	healthy := f.monitor(t, "saudável")
	broken := f.monitor(t, "quebrado")
	f.seed(t, healthy, hourAt(-1), 60, domain.StatusUp, 100)
	f.seed(t, broken, hourAt(-1), 60, domain.StatusDown, 0)

	f.runCycle(t)

	up := f.rollups(t, healthy, domain.ResolutionHourly)
	down := f.rollups(t, broken, domain.ResolutionHourly)

	if len(up) != 1 || up[0].UptimePercent() != 100 {
		t.Errorf("healthy monitor uptime = %v, want 100", up)
	}
	if len(down) != 1 || down[0].UptimePercent() != 0 {
		t.Errorf("broken monitor uptime = %v, want 0", down)
	}
}

// ---------- retenção ----------

func TestWorkerPrunesRawBeyondRetention(t *testing.T) {
	ret := defaultRetention()
	ret.Raw = 2 * time.Hour
	f := newFixture(t, ret)
	id := f.monitor(t, "api")

	f.seed(t, id, hourAt(-5), 60, domain.StatusUp, 100) // fora da janela
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100) // dentro

	f.runCycle(t)

	if got := f.heartbeatCount(t, id); got != 60 {
		t.Errorf("kept %d raw heartbeats, want 60: only the ones inside the window", got)
	}
}

// A prova central do design: o dado cru some, mas o histórico agregado
// permanece. É o que faz o banco parar de crescer sem perder o gráfico
// de meses.
func TestWorkerKeepsAggregatesAfterPruningRaw(t *testing.T) {
	ret := defaultRetention()
	ret.Raw = 2 * time.Hour
	f := newFixture(t, ret)
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-5), 60, domain.StatusUp, 100)

	f.runCycle(t)

	if got := f.heartbeatCount(t, id); got != 0 {
		t.Errorf("kept %d raw heartbeats past the retention window, want 0", got)
	}

	hourly := f.rollups(t, id, domain.ResolutionHourly)
	if len(hourly) != 1 {
		t.Fatalf("produced %d hourly rollups, want 1: aggregates must outlive the raw data", len(hourly))
	}
	if hourly[0].Total != 60 {
		t.Errorf("Total = %d, want 60", hourly[0].Total)
	}
	if hourly[0].LatencyP95MS != 100 {
		t.Errorf("LatencyP95MS = %v, want 100", hourly[0].LatencyP95MS)
	}
}

// Se a poda rodasse antes da agregação, este dado sumiria para sempre sem
// nunca ter virado estatística. É a invariante mais cara de violar do
// projeto inteiro.
func TestWorkerAggregatesBeforePruning(t *testing.T) {
	ret := defaultRetention()
	// Janela curtíssima: tudo o que foi semeado já nasce elegível à poda.
	ret.Raw = time.Minute
	f := newFixture(t, ret)
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-3), 60, domain.StatusUp, 250)

	f.runCycle(t)

	if got := f.heartbeatCount(t, id); got != 0 {
		t.Fatalf("kept %d raw heartbeats, want them all pruned by this retention", got)
	}

	hourly := f.rollups(t, id, domain.ResolutionHourly)
	if len(hourly) != 1 {
		t.Fatalf("produced %d hourly rollups, want 1: the data was pruned before being aggregated", len(hourly))
	}
	if hourly[0].Total != 60 || hourly[0].LatencyAvgMS != 250 {
		t.Errorf("rollup = (total %d, avg %v), want (60, 250)", hourly[0].Total, hourly[0].LatencyAvgMS)
	}
}

// Cada camada tem sua própria janela: a horária expira antes da diária,
// que é a que sustenta o gráfico de meses.
func TestWorkerPrunesEachResolutionOnItsOwnWindow(t *testing.T) {
	ret := rollup.Retention{
		Raw:    time.Hour,
		Hourly: 2 * time.Hour,
		Daily:  365 * 24 * time.Hour,
	}
	f := newFixture(t, ret)
	id := f.monitor(t, "api")

	// Um dia antigo, já encerrado.
	dayStart := now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	for h := 0; h < 24; h++ {
		f.seed(t, id, dayStart.Add(time.Duration(h)*time.Hour), 60, domain.StatusUp, 100)
	}

	f.runCycle(t)

	if got := f.rollups(t, id, domain.ResolutionHourly); len(got) != 0 {
		t.Errorf("kept %d hourly rollups past their window, want 0", len(got))
	}
	if got := f.rollups(t, id, domain.ResolutionDaily); len(got) != 1 {
		t.Errorf("kept %d daily rollups, want 1: the daily window is much longer", len(got))
	}
}

func TestWorkerRunLoopsUntilContextEnds(t *testing.T) {
	f := newFixture(t, defaultRetention())
	id := f.monitor(t, "api")
	f.seed(t, id, hourAt(-1), 60, domain.StatusUp, 100)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.worker.Run(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.rollups(t, id, domain.ResolutionHourly)) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	if got := f.rollups(t, id, domain.ResolutionHourly); len(got) != 1 {
		t.Errorf("produced %d rollups from the running loop, want 1", len(got))
	}
}

func TestWorkerAppliesDefaultOptions(t *testing.T) {
	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer st.Close()

	w := rollup.NewWorker(st, rollup.Options{})

	if w.Interval() != rollup.DefaultInterval {
		t.Errorf("Interval() = %v, want %v", w.Interval(), rollup.DefaultInterval)
	}
	ret := w.Retention()
	if ret.Raw != rollup.DefaultRetention.Raw {
		t.Errorf("Retention().Raw = %v, want %v", ret.Raw, rollup.DefaultRetention.Raw)
	}
	if ret.Hourly != rollup.DefaultRetention.Hourly {
		t.Errorf("Retention().Hourly = %v, want %v", ret.Hourly, rollup.DefaultRetention.Hourly)
	}
}

// Reter o agregado horário por menos tempo que o cru inverteria a
// cascata: o dado detalhado sobreviveria ao resumo que deveria substituí-lo.
func TestRetentionValidateRejectsInvertedCascade(t *testing.T) {
	tests := []struct {
		name string
		ret  rollup.Retention
	}{
		{
			"horário menor que cru",
			rollup.Retention{Raw: 30 * 24 * time.Hour, Hourly: 24 * time.Hour, Daily: 365 * 24 * time.Hour},
		},
		{
			"diário menor que horário",
			rollup.Retention{Raw: time.Hour, Hourly: 90 * 24 * time.Hour, Daily: 24 * time.Hour},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.ret.Validate(); err == nil {
				t.Error("Validate() returned nil error, want an error")
			}
		})
	}
}

func TestRetentionValidateAcceptsProperCascade(t *testing.T) {
	if err := defaultRetention().Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestRetentionValidateRejectsNonPositiveWindow(t *testing.T) {
	ret := defaultRetention()
	ret.Raw = 0

	if err := ret.Validate(); err == nil {
		t.Error("Validate() returned nil error for a zero window, want an error")
	}
}
