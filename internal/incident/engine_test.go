package incident_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/incident"
	"github.com/bernardojoao/upwatch/internal/notifier"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

// recordingSink guarda o que passou por ele e pode falhar sob comando.
type recordingSink struct {
	mu   sync.Mutex
	got  []domain.Heartbeat
	fail error
}

func (r *recordingSink) Submit(_ context.Context, hb domain.Heartbeat) error {
	r.mu.Lock()
	r.got = append(r.got, hb)
	r.mu.Unlock()
	return r.fail
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// recordingDispatcher captura os avisos enfileirados.
type recordingDispatcher struct {
	mu       sync.Mutex
	sent     []notifier.Notification
	channels [][]notifier.Notifier
}

func (d *recordingDispatcher) Enqueue(n notifier.Notification, channels []notifier.Notifier) {
	d.mu.Lock()
	d.sent = append(d.sent, n)
	d.channels = append(d.channels, channels)
	d.mu.Unlock()
}

func (d *recordingDispatcher) all() []notifier.Notification {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]notifier.Notification(nil), d.sent...)
}

// engineFixture reúne o motor com um store real.
type engineFixture struct {
	store    *sqlstore.Store
	sink     *recordingSink
	dispatch *recordingDispatcher
	engine   *incident.Engine
	monitor  domain.Monitor
}

func newEngine(t *testing.T) *engineFixture {
	t.Helper()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := domain.Monitor{
		Name: "api", Type: domain.MonitorHTTP, Target: "https://exemplo.com",
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: threshold, Enabled: true,
	}
	if err := st.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	sink := &recordingSink{}
	dispatch := &recordingDispatcher{}
	eng := incident.NewEngine(sink, st, dispatch, nil)
	eng.Upsert(m)

	return &engineFixture{store: st, sink: sink, dispatch: dispatch, engine: eng, monitor: m}
}

// observe alimenta o motor com uma sequência, uma batida por minuto.
func (f *engineFixture) observe(t *testing.T, statuses ...domain.Status) {
	t.Helper()

	for i, status := range statuses {
		hb := domain.Heartbeat{
			MonitorID: f.monitor.ID,
			Timestamp: epoch.Add(time.Duration(i) * time.Minute),
			Status:    status,
			Message:   "status 502 fora do esperado",
		}
		if err := f.engine.Submit(context.Background(), hb); err != nil {
			t.Fatalf("Submit returned unexpected error: %v", err)
		}
	}
}

// linkChannel cria um canal e o vincula ao monitor.
func (f *engineFixture) linkChannel(t *testing.T) domain.Channel {
	t.Helper()

	c := domain.Channel{
		Name: "discord", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := f.store.Channels().Create(context.Background(), &c); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if err := f.store.Channels().Link(context.Background(), f.monitor.ID, c.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}
	return c
}

// ---------- encaminhamento ----------

// A batida precisa chegar ao banco independentemente do que o motor
// decida: gravar a medição é o caminho crítico.
func TestEngineForwardsEveryHeartbeat(t *testing.T) {
	f := newEngine(t)

	f.observe(t, domain.StatusUp, domain.StatusDown, domain.StatusUp)

	if f.sink.count() != 3 {
		t.Errorf("forwarded %d heartbeats, want 3", f.sink.count())
	}
}

func TestEngineReportsSinkFailure(t *testing.T) {
	f := newEngine(t)
	f.sink.fail = errors.New("banco indisponível")

	err := f.engine.Submit(context.Background(), domain.Heartbeat{
		MonitorID: f.monitor.ID, Timestamp: epoch, Status: domain.StatusUp,
	})

	if err == nil {
		t.Fatal("Submit swallowed the sink failure, want it propagated")
	}
}

// Batida de monitor desconhecido não pode derrubar nem inventar limiar.
func TestEngineIgnoresUnknownMonitor(t *testing.T) {
	f := newEngine(t)

	err := f.engine.Submit(context.Background(), domain.Heartbeat{
		MonitorID: 9999, Timestamp: epoch, Status: domain.StatusDown,
	})

	if err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}
	if len(f.dispatch.all()) != 0 {
		t.Error("an unknown monitor produced a notification")
	}
}

// ---------- incidentes ----------

func TestEngineOpensIncidentOnConfirmedOutage(t *testing.T) {
	f := newEngine(t)

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	got, err := f.store.Incidents().Current(context.Background(), f.monitor.ID)
	if err != nil {
		t.Fatalf("Current returned unexpected error: %v", err)
	}
	if !got.Open() {
		t.Error("the incident is not open")
	}
	if got.Cause == "" {
		t.Error("the incident carries no cause; the operator would not know what happened")
	}
}

func TestEngineDoesNotOpenBeforeTheThreshold(t *testing.T) {
	f := newEngine(t)

	f.observe(t, repeatStatus(threshold-1, domain.StatusDown)...)

	if _, err := f.store.Incidents().Current(context.Background(), f.monitor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an incident was opened before the threshold: %v", err)
	}
}

func TestEngineResolvesIncidentOnRecovery(t *testing.T) {
	f := newEngine(t)
	sequencia := append(repeatStatus(threshold, domain.StatusDown), repeatStatus(threshold, domain.StatusUp)...)

	f.observe(t, sequencia...)

	if _, err := f.store.Incidents().Current(context.Background(), f.monitor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the incident is still open after recovery: %v", err)
	}

	page, err := f.store.Incidents().List(context.Background(), store.IncidentFilter{MonitorID: f.monitor.ID})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Open() {
		t.Fatalf("incidents = %v, want one closed", page.Items)
	}
	if got := page.Items[0].Duration(epoch); got != 3*time.Minute {
		t.Errorf("Duration = %v, want 3m", got)
	}
}

// ---------- estado ----------

// Sem persistir, um reinício zeraria a contagem e a detecção atrasaria
// justamente depois de uma manutenção.
func TestEngineRestoresStateOnLoad(t *testing.T) {
	f := newEngine(t)
	ctx := context.Background()

	// Duas falhas: falta uma para confirmar.
	f.observe(t, repeatStatus(threshold-1, domain.StatusDown)...)

	// Um motor novo sobre o mesmo banco, como aconteceria num reinício.
	novo := incident.NewEngine(&recordingSink{}, f.store, f.dispatch, nil)
	novo.Upsert(f.monitor)
	if err := novo.Load(ctx); err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if got := novo.StateOf(f.monitor.ID); got.Consecutive != threshold-1 {
		t.Errorf("Consecutive after restart = %d, want the count to survive at %d",
			got.Consecutive, threshold-1)
	}

	// A próxima falha confirma, sem recomeçar a contagem.
	hb := domain.Heartbeat{
		MonitorID: f.monitor.ID,
		Timestamp: epoch.Add(time.Hour),
		Status:    domain.StatusDown,
	}
	if err := novo.Submit(ctx, hb); err != nil {
		t.Fatalf("Submit returned unexpected error: %v", err)
	}

	if _, err := f.store.Incidents().Current(ctx, f.monitor.ID); err != nil {
		t.Errorf("the incident was not opened after restart: %v", err)
	}
}

func TestEngineForgetsRemovedMonitor(t *testing.T) {
	f := newEngine(t)
	f.observe(t, repeatStatus(threshold-1, domain.StatusDown)...)

	f.engine.Remove(f.monitor.ID)

	if got := f.engine.StateOf(f.monitor.ID); got.Consecutive != 0 {
		t.Errorf("state survived Remove: %+v", got)
	}
}

// ---------- avisos ----------

func TestEngineNotifiesOnConfirmedOutage(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	avisos := f.dispatch.all()
	if len(avisos) != 1 {
		t.Fatalf("dispatched %d notifications, want 1", len(avisos))
	}
	if avisos[0].Event.Kind != incident.KindDown {
		t.Errorf("notification kind = %v, want %v", avisos[0].Event.Kind, incident.KindDown)
	}
	if avisos[0].Monitor.Name != "api" {
		t.Errorf("notification monitor = %q, want %q", avisos[0].Monitor.Name, "api")
	}
	// A causa é o que decide se alguém precisa levantar da cadeira.
	if avisos[0].Message == "" {
		t.Error("the notification carries no cause")
	}
}

func TestEngineNotifiesOnRecoveryWithDuration(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)
	sequencia := append(repeatStatus(threshold, domain.StatusDown), repeatStatus(threshold, domain.StatusUp)...)

	f.observe(t, sequencia...)

	avisos := f.dispatch.all()
	if len(avisos) != 2 {
		t.Fatalf("dispatched %d notifications, want 2", len(avisos))
	}
	volta := avisos[1]
	if volta.Event.Kind != incident.KindUp {
		t.Errorf("second notification kind = %v, want %v", volta.Event.Kind, incident.KindUp)
	}
	if volta.Event.Duration != 3*time.Minute {
		t.Errorf("Duration = %v, want 3m", volta.Event.Duration)
	}
}

// Monitor sem canal vinculado não gera entrega: enfileirar sem destino só
// gastaria a fila.
func TestEngineSkipsNotificationWithoutChannels(t *testing.T) {
	f := newEngine(t)

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	if len(f.dispatch.all()) != 0 {
		t.Errorf("dispatched %d notifications with no channel linked, want 0", len(f.dispatch.all()))
	}
}

// Um canal desligado não deve receber nada, e o filtro precisa valer aqui
// e não só na interface.
func TestEngineSkipsDisabledChannel(t *testing.T) {
	f := newEngine(t)
	canal := f.linkChannel(t)

	canal.Enabled = false
	if err := f.store.Channels().Update(context.Background(), canal); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	if len(f.dispatch.all()) != 0 {
		t.Errorf("dispatched %d notifications to a disabled channel, want 0", len(f.dispatch.all()))
	}
}

// Queda longa não pode repetir o aviso a cada verificação.
func TestEngineDoesNotRepeatTheAlert(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)

	f.observe(t, repeatStatus(threshold+15, domain.StatusDown)...)

	if len(f.dispatch.all()) != 1 {
		t.Errorf("dispatched %d notifications for a sustained outage, want 1", len(f.dispatch.all()))
	}
}

// Canal mal configurado não pode calar os demais nem derrubar o motor.
func TestEngineSurvivesMalformedChannel(t *testing.T) {
	f := newEngine(t)
	quebrado := domain.Channel{
		Name: "quebrado", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{}`), // sem url
	}
	if err := f.store.Channels().Create(context.Background(), &quebrado); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if err := f.store.Channels().Link(context.Background(), f.monitor.ID, quebrado.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}

	f.observe(t, repeatStatus(threshold, domain.StatusDown)...)

	avisos := f.dispatch.all()
	if len(avisos) != 1 {
		t.Fatalf("dispatched %d notifications, want the enqueue to still happen", len(avisos))
	}
	if len(f.dispatch.channels[0]) != 0 {
		t.Errorf("the malformed channel was included among the destinations")
	}
}

// A sentinela converte falha em desconhecido quando a rede do próprio
// monitor caiu; se isso gerasse alerta, a supressão não serviria de nada.
func TestEngineStaysSilentForUnmeasuredSamples(t *testing.T) {
	f := newEngine(t)
	f.linkChannel(t)

	f.observe(t, repeatStatus(threshold+5, domain.StatusUnknown)...)

	if len(f.dispatch.all()) != 0 {
		t.Errorf("dispatched %d notifications for unmeasured samples, want silence", len(f.dispatch.all()))
	}
	if _, err := f.store.Incidents().Current(context.Background(), f.monitor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("an incident was opened from samples that measured nothing")
	}
}

func repeatStatus(n int, status domain.Status) []domain.Status {
	out := make([]domain.Status, n)
	for i := range out {
		out[i] = status
	}
	return out
}
