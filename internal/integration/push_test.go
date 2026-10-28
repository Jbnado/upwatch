package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// Do sinal ao aviso: o push com estado atravessa a cadeia inteira.
//
// Os testes de unidade provam que o endpoint grava a causa, que o checker
// a devolve e que o motor confirma quedas. Este prova que as peças se
// encaixam: um script que diz "fila sem worker" faz chegar exatamente essa
// causa no aviso — e só depois da confirmação, como qualquer outra falha.

// avisos captura o que o motor mandaria aos canais.
type avisos struct {
	mu   sync.Mutex
	sent []notifier.Notification
}

func (a *avisos) Enqueue(n notifier.Notification, _ []notifier.Notifier) {
	a.mu.Lock()
	a.sent = append(a.sent, n)
	a.mu.Unlock()
}

func (a *avisos) all() []notifier.Notification {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]notifier.Notification(nil), a.sent...)
}

// gravacao entrega a batida ao banco, como o escritor de lote faria. É
// por ela que o teste vê a latência que de fato vai para os percentis.
type gravacao struct{ st *sqlstore.Store }

func (g gravacao) Submit(ctx context.Context, hb domain.Heartbeat) error {
	return g.st.WriteHeartbeats(ctx, []domain.Heartbeat{hb})
}

const pushToken = "um-token-suficientemente-longo"

// cadeia é a montagem inteira: endpoint, checker, motor e banco.
type cadeia struct {
	t        *testing.T
	st       *sqlstore.Store
	fake     *clock.Fake
	srv      *httptest.Server
	push     *checker.Push
	motor    *incident.Engine
	enviados *avisos
	m        domain.Monitor
}

// novaCadeia monta a cadeia com um monitor push de intervalo de um minuto
// e a confirmação pedida. Sem folga explícita, a janela é de dois minutos.
func novaCadeia(t *testing.T, confirmacao int) *cadeia {
	t.Helper()
	ctx := context.Background()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	m := domain.Monitor{
		Name: "fila de pedidos", Type: domain.MonitorPush,
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: confirmacao, Enabled: true,
		Config: json.RawMessage(`{"token":"` + pushToken + `"}`),
	}
	if err := st.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	// O relógio começa na criação, que o banco marca com a hora real: é
	// dela que a espera do push conta, e um relógio falso atrás dela
	// faria a janela nunca terminar.
	fake := clock.NewFake(m.CreatedAt)

	canal := domain.Channel{
		Name: "plantão", Type: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://exemplo.invalido/hook"}`),
	}
	if err := st.Channels().Create(ctx, &canal); err != nil {
		t.Fatalf("Create channel returned unexpected error: %v", err)
	}
	if err := st.Channels().Link(ctx, m.ID, canal.ID); err != nil {
		t.Fatalf("Link returned unexpected error: %v", err)
	}

	push := checker.NewPush(st, fake)
	reg, err := checker.NewRegistry(push)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	srv := httptest.NewServer(api.New(api.Options{
		Store: st, Auth: auth.New(st, auth.Options{Clock: fake}), Checkers: reg, Clock: fake,
	}))
	t.Cleanup(srv.Close)

	enviados := &avisos{}
	motor := incident.NewEngine(gravacao{st}, st, enviados, nil)
	motor.Upsert(m)

	return &cadeia{t: t, st: st, fake: fake, srv: srv, push: push, motor: motor, enviados: enviados, m: m}
}

// sinal é o script reportando, pelo caminho documentado.
func (c *cadeia) sinal(query string) {
	c.t.Helper()
	resp, err := http.Post(c.srv.URL+"/api/v1/push/"+pushToken+query, "", nil)
	if err != nil {
		c.t.Fatalf("push returned unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("push status = %d, want 200", resp.StatusCode)
	}
}

// verificar faz o que o agendador faz a cada intervalo.
func (c *cadeia) verificar() {
	c.t.Helper()
	res := c.push.Check(context.Background(), c.m)
	if err := c.motor.Submit(context.Background(), domain.Heartbeat{
		MonitorID: c.m.ID, Timestamp: c.fake.Now(), Status: res.Status,
		LatencyMS: res.LatencyMS, Message: res.Message, Repeated: res.Repeated,
	}); err != nil {
		c.t.Fatalf("Submit returned unexpected error: %v", err)
	}
}

// semIncidente falha se a queda já tiver sido confirmada.
func (c *cadeia) semIncidente(quando string) {
	c.t.Helper()
	if _, err := c.st.Incidents().Current(context.Background(), c.m.ID); !errors.Is(err, store.ErrNotFound) {
		c.t.Fatalf("an incident opened %s: %v", quando, err)
	}
	if n := len(c.enviados.all()); n != 0 {
		c.t.Fatalf("%d notification(s) went out %s", n, quando)
	}
}

// Com confirmação 2 são precisos DOIS sinais de queda. Um sinal só, relido
// a cada verificação enquanto a janela dura, não pode bastar: cada
// releitura repete o que o script disse uma vez, e contá-las faria um
// soluço do script abrir incidente sozinho — exatamente o que o limiar
// existe para impedir.
func TestPushReportedDownOpensIncidentOnlyAfterNSignals(t *testing.T) {
	c := novaCadeia(t, 2)

	c.sinal("?status=down&msg=fila+sem+worker")
	c.verificar()
	c.semIncidente("after one down signal")

	// Várias verificações dentro da janela, sem sinal novo: a mesma queda
	// relida não conta de novo.
	for range 5 {
		c.fake.Advance(15 * time.Second)
		c.verificar()
	}
	c.semIncidente("from re-reading a single down signal")

	// O segundo sinal de queda é a segunda observação: confirmada.
	c.sinal("?status=down&msg=fila+sem+worker")
	c.verificar()

	aberto, err := c.st.Incidents().Current(context.Background(), c.m.ID)
	if err != nil {
		t.Fatalf("no incident after the second down signal: %v", err)
	}
	if aberto.Cause != "fila sem worker" {
		t.Errorf("incident cause = %q, want the cause reported by the script", aberto.Cause)
	}
	got := c.enviados.all()
	if len(got) != 1 {
		t.Fatalf("sent %d notifications, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "fila sem worker") {
		t.Errorf("notification message = %q, want the reported cause", got[0].Message)
	}
}

// Um "down" seguido de silêncio cai pela regra do silêncio: depois que a
// janela do sinal passa, cada verificação mede de novo a falta de sinal, e
// é isso que completa a confirmação — não a releitura do "down". A causa
// do incidente diz que o script parou de reportar, que é o problema agora.
func TestPushSingleDownThenSilenceFallsByTheSilenceRule(t *testing.T) {
	c := novaCadeia(t, 2)

	c.sinal("?status=down&msg=fila+sem+worker")
	c.verificar()

	// Até o fim da janela (intervalo mais folga, dois minutos), só
	// releituras.
	for range 3 {
		c.fake.Advance(30 * time.Second)
		c.verificar()
	}
	c.semIncidente("within the signal window")

	c.fake.Advance(31 * time.Second)
	c.verificar()

	aberto, err := c.st.Incidents().Current(context.Background(), c.m.ID)
	if err != nil {
		t.Fatalf("no incident after the window ran out: %v", err)
	}
	if !strings.Contains(aberto.Cause, "sem sinal") {
		t.Errorf("incident cause = %q, want the silence rule to be the one that fired", aberto.Cause)
	}
}

// A latência informada é uma medição do sinal, não de cada verificação.
// Copiá-la para toda batida até o sinal seguinte encheria os percentis com
// a mesma amostra repetida, e o p95 refletiria quantas vezes o sinal foi
// relido, e não quanto a tarefa demorou.
func TestPushLatencyIsRecordedOncePerSignal(t *testing.T) {
	c := novaCadeia(t, 2)
	inicio := c.fake.Now()

	c.sinal("?latency_ms=842")
	c.verificar()
	for range 3 {
		c.fake.Advance(15 * time.Second)
		c.verificar()
	}

	batidas, err := c.st.QueryHeartbeats(context.Background(), store.HeartbeatQuery{
		MonitorID: c.m.ID,
		Range:     store.TimeRange{From: inicio.Add(-time.Minute), To: c.fake.Now().Add(time.Minute)},
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(batidas) != 4 {
		t.Fatalf("got %d heartbeats, want 4", len(batidas))
	}
	medidas := 0
	for _, hb := range batidas {
		if hb.LatencyMS != 0 {
			medidas++
			if hb.LatencyMS != 842 {
				t.Errorf("heartbeat latency = %d, want 842", hb.LatencyMS)
			}
		}
	}
	if medidas != 1 {
		t.Errorf("%d heartbeats carry the latency, want only the first after the signal", medidas)
	}
}
