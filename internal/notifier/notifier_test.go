package notifier_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/notifier"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// outage monta a notificação de uma queda confirmada.
func outage() notifier.Notification {
	return notifier.Notification{
		Monitor: domain.Monitor{ID: 7, Name: "api de produção", Target: "https://api.exemplo.com/health"},
		Event: incident.Event{
			Kind: incident.KindDown,
			From: domain.StatusUp,
			To:   domain.StatusDown,
			At:   epoch,
		},
		Message: "status 502 fora do esperado",
	}
}

// recovery monta a notificação de uma volta, com duração.
func recovery(d time.Duration) notifier.Notification {
	n := outage()
	n.Event = incident.Event{
		Kind: incident.KindUp, From: domain.StatusDown, To: domain.StatusUp,
		At: epoch.Add(d), Duration: d,
	}
	n.Message = ""
	return n
}

// capture recebe as entregas para inspeção.
type capture struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string

	status atomic.Int32
	hits   atomic.Int32
}

func newCapture(t *testing.T) (*capture, string) {
	t.Helper()

	c := &capture{}
	c.status.Store(http.StatusOK)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits.Add(1)
		corpo, _ := io.ReadAll(r.Body)

		c.mu.Lock()
		c.requests = append(c.requests, r.Clone(context.Background()))
		c.bodies = append(c.bodies, string(corpo))
		c.mu.Unlock()

		w.WriteHeader(int(c.status.Load()))
	}))
	t.Cleanup(srv.Close)

	return c, srv.URL
}

func (c *capture) body(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.bodies) {
		return ""
	}
	return c.bodies[i]
}

func send(t *testing.T, n notifier.Notifier, note notifier.Notification) error {
	t.Helper()
	return n.Send(context.Background(), note)
}

// ---------- webhook genérico ----------

func TestWebhookPostsJSON(t *testing.T) {
	cap, url := newCapture(t)
	w, err := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}

	if err := send(t, w, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	if cap.hits.Load() != 1 {
		t.Fatalf("the endpoint received %d requests, want 1", cap.hits.Load())
	}

	var corpo map[string]any
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	if corpo["monitor"] != "api de produção" {
		t.Errorf("payload monitor = %v, want the monitor name", corpo["monitor"])
	}
	if corpo["status"] != "down" {
		t.Errorf("payload status = %v, want down", corpo["status"])
	}
}

// Sem a duração no corpo, quem recebe precisa ir até a interface só para
// descobrir há quanto tempo o serviço está fora.
func TestWebhookIncludesOutageDuration(t *testing.T) {
	cap, url := newCapture(t)
	w, _ := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `"}`))

	if err := send(t, w, recovery(17*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)

	if corpo["duration_seconds"] != float64(17*60) {
		t.Errorf("duration_seconds = %v, want %v", corpo["duration_seconds"], 17*60)
	}
}

func TestWebhookSendsConfiguredHeaders(t *testing.T) {
	cap, url := newCapture(t)
	cfg := `{"url":"` + url + `","headers":{"X-Token":"segredo"}}`
	w, err := notifier.NewWebhook(json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}

	if err := send(t, w, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if got := cap.requests[0].Header.Get("X-Token"); got != "segredo" {
		t.Errorf("X-Token = %q, want %q", got, "segredo")
	}
}

// Resposta de erro precisa virar erro para a fila poder repetir; engolir
// faria a notificação sumir em silêncio.
func TestWebhookReportsServerFailure(t *testing.T) {
	cap, url := newCapture(t)
	cap.status.Store(http.StatusInternalServerError)
	w, _ := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `"}`))

	if err := send(t, w, outage()); err == nil {
		t.Fatal("Send returned nil error for a 500 response, want an error")
	}
}

func TestWebhookRequiresURL(t *testing.T) {
	if _, err := notifier.NewWebhook(json.RawMessage(`{}`)); err == nil {
		t.Fatal("NewWebhook without a URL returned nil error, want an error")
	}
}

func TestWebhookRejectsMalformedConfig(t *testing.T) {
	if _, err := notifier.NewWebhook(json.RawMessage(`{nope`)); err == nil {
		t.Fatal("NewWebhook with malformed config returned nil error, want an error")
	}
}

// ---------- Discord e Slack ----------

func TestDiscordSendsReadableContent(t *testing.T) {
	cap, url := newCapture(t)
	d, err := notifier.NewDiscord(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewDiscord returned unexpected error: %v", err)
	}

	if err := send(t, d, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	corpo := cap.body(0)
	if !strings.Contains(corpo, "api de produção") {
		t.Errorf("the message omits the monitor name: %s", corpo)
	}
	// A causa é o que decide se alguém precisa levantar da cadeira.
	if !strings.Contains(corpo, "502") {
		t.Errorf("the message omits the failure reason: %s", corpo)
	}
}

func TestSlackSendsReadableContent(t *testing.T) {
	cap, url := newCapture(t)
	s, err := notifier.NewSlack(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewSlack returned unexpected error: %v", err)
	}

	if err := send(t, s, recovery(3*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	corpo := cap.body(0)
	if !strings.Contains(corpo, "api de produção") {
		t.Errorf("the message omits the monitor name: %s", corpo)
	}
	if !strings.Contains(corpo, "3 min") {
		t.Errorf("the recovery message omits how long it was down: %s", corpo)
	}
}

// ---------- modelo de mensagem ----------

func TestRenderDescribesAnOutage(t *testing.T) {
	texto := notifier.Render(outage())

	for _, esperado := range []string{"api de produção", "fora do ar", "502"} {
		if !strings.Contains(texto, esperado) {
			t.Errorf("the rendered text omits %q: %s", esperado, texto)
		}
	}
}

func TestRenderDescribesARecoveryWithDuration(t *testing.T) {
	texto := notifier.Render(recovery(2*time.Hour + 5*time.Minute))

	if !strings.Contains(texto, "voltou") {
		t.Errorf("the rendered text does not say it recovered: %s", texto)
	}
	if !strings.Contains(texto, "2 h") {
		t.Errorf("the rendered text omits the outage duration: %s", texto)
	}
}

// Um modelo próprio permite adaptar a mensagem ao canal e ao time.
func TestCustomTemplateIsApplied(t *testing.T) {
	cap, url := newCapture(t)
	cfg := `{"url":"` + url + `","template":"ALERTA {{.Monitor.Name}} => {{.Status}}"}`
	w, err := notifier.NewWebhook(json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}

	if err := send(t, w, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)

	if corpo["text"] != "ALERTA api de produção => down" {
		t.Errorf("text = %v, want the custom template applied", corpo["text"])
	}
}

// Modelo inválido precisa reprovar no cadastro do canal, não na hora do
// incidente — que é justamente quando a mensagem não pode falhar.
func TestInvalidTemplateIsRejectedUpFront(t *testing.T) {
	cfg := `{"url":"http://exemplo.invalido","template":"{{.Naofecha"}`

	if _, err := notifier.NewWebhook(json.RawMessage(cfg)); err == nil {
		t.Fatal("NewWebhook with a malformed template returned nil error, want an error")
	}
}

// ---------- registro ----------

func TestBuildResolvesEachChannelType(t *testing.T) {
	for _, tipo := range []string{"webhook", "discord", "slack"} {
		t.Run(tipo, func(t *testing.T) {
			n, err := notifier.Build(tipo, json.RawMessage(`{"url":"http://exemplo.invalido"}`))
			if err != nil {
				t.Fatalf("Build(%q) returned unexpected error: %v", tipo, err)
			}
			if n == nil {
				t.Fatalf("Build(%q) returned nil", tipo)
			}
		})
	}
}

func TestBuildRejectsUnknownType(t *testing.T) {
	if _, err := notifier.Build("pombo-correio", json.RawMessage(`{}`)); err == nil {
		t.Fatal("Build with an unknown type returned nil error, want an error")
	}
}
