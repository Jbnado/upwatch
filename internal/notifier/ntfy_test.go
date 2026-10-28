package notifier_test

import (
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
)

func newNtfy(t *testing.T, cfg string) notifier.Notifier {
	t.Helper()
	n, err := notifier.NewNtfy(json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("NewNtfy returned unexpected error: %v", err)
	}
	return n
}

func (c *capture) request(i int) *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i >= len(c.requests) {
		return nil
	}
	return c.requests[i]
}

// titulo decodifica o Title, que vai em RFC 2047 quando tem acento: o
// ntfy decodifica, e cabeçalho HTTP com byte fora do ASCII não tem
// garantia de chegar inteiro.
func titulo(t *testing.T, r *http.Request) string {
	t.Helper()
	s, err := new(mime.WordDecoder).DecodeHeader(r.Header.Get("Title"))
	if err != nil {
		t.Fatalf("decoding Title: %v", err)
	}
	return s
}

// A queda precisa furar o modo silencioso do celular; é para isso que se
// instala o ntfy. A mensagem é o que aparece na notificação, então é a
// frase com a causa — não JSON.
func TestNtfyPublishesOutageWithHighPriority(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"upwatch-alertas"}`)

	if err := send(t, n, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	r := cap.request(0)
	if r == nil {
		t.Fatal("the ntfy server received no request")
	}
	if r.Method != http.MethodPost || r.URL.Path != "/upwatch-alertas" {
		t.Errorf("request = %s %s, want POST /upwatch-alertas", r.Method, r.URL.Path)
	}
	if got := titulo(t, r); got != "api de produção: fora do ar" {
		t.Errorf("Title = %q, want the monitor name and the state", got)
	}
	if p := r.Header.Get("Priority"); p != "4" && p != "5" {
		t.Errorf("Priority = %q, want high (4 or 5) on an outage", p)
	}
	if tags := r.Header.Get("Tags"); tags == "" {
		t.Error("Tags is empty, want an icon that tells outage from recovery at a glance")
	}
	if corpo := cap.body(0); !strings.Contains(corpo, "502") {
		t.Errorf("the message omits the failure reason: %q", corpo)
	}
	// Sem token configurado, nada de Authorization: um cabeçalho vazio
	// faz servidor próprio com controle de acesso recusar o que aceitaria
	// como anônimo.
	if a := r.Header.Get("Authorization"); a != "" {
		t.Errorf("Authorization = %q, want none without a token", a)
	}
}

// A volta é boa notícia: chegar com a mesma urgência da queda ensinaria a
// ignorar as duas.
func TestNtfyPublishesRecoveryWithNormalPriority(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"upwatch-alertas"}`)

	if err := send(t, n, recovery(9*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	r := cap.request(0)
	if p := r.Header.Get("Priority"); p != "3" {
		t.Errorf("Priority = %q, want 3 (normal) on recovery", p)
	}
	if got := titulo(t, r); got != "api de produção: no ar" {
		t.Errorf("Title = %q, want the recovery state", got)
	}
	if r.Header.Get("Tags") == "" {
		t.Error("Tags is empty on recovery")
	}
	if corpo := cap.body(0); !strings.Contains(corpo, "9 min") {
		t.Errorf("the recovery message omits the outage duration: %q", corpo)
	}
}

// Queda e volta precisam ter ícones diferentes: na tela de bloqueio, o
// ícone é o que se lê antes do texto.
func TestNtfyTagsTellOutageFromRecovery(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t"}`)

	_ = send(t, n, outage())
	_ = send(t, n, recovery(time.Minute))

	if cap.request(0).Header.Get("Tags") == cap.request(1).Header.Get("Tags") {
		t.Errorf("outage and recovery share the tags %q", cap.request(0).Header.Get("Tags"))
	}
}

// Degradado não é queda: acordar alguém por latência alta com a mesma
// prioridade de "fora do ar" gasta a atenção que a queda vai precisar.
func TestNtfyDegradedIsNotAsUrgentAsDown(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t"}`)

	degradado := outage()
	degradado.Event.Kind = domain.ChangeDegraded
	degradado.Event.To = domain.StatusDegraded
	_ = send(t, n, degradado)

	if p := cap.request(0).Header.Get("Priority"); p == "4" || p == "5" {
		t.Errorf("Priority = %q on degraded, want below the outage priority", p)
	}
}

// Servidor próprio com controle de acesso exige token; sem ele o canal
// ficaria restrito ao servidor público.
func TestNtfySendsBearerToken(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t","token":"tk_segredo"}`)

	if err := send(t, n, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	if a := cap.request(0).Header.Get("Authorization"); a != "Bearer tk_segredo" {
		t.Errorf("Authorization = %q, want the bearer token", a)
	}
}

// Token recusado é erro de configuração, e o botão de teste é onde se
// descobre isso. "respondeu 403" não diz o que fazer.
func TestNtfyExplainsRefusedToken(t *testing.T) {
	cap, url := newCapture(t)
	cap.status.Store(http.StatusForbidden)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t","token":"tk_errado"}`)

	err := send(t, n, outage())
	if err == nil {
		t.Fatal("Send with a refused token returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("error = %q, want it to point at the token", err)
	}
	if strings.Contains(err.Error(), "tk_errado") {
		t.Errorf("error = %q leaks the token", err)
	}
}

// Falha do servidor precisa virar erro para a fila repetir a entrega.
func TestNtfyReportsServerFailure(t *testing.T) {
	cap, url := newCapture(t)
	cap.status.Store(http.StatusInternalServerError)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t"}`)

	if err := send(t, n, outage()); err == nil {
		t.Fatal("Send returned nil error for a 500 response, want an error")
	}
}

// Sem servidor configurado vale o público: é o caminho mais curto até o
// alerta no celular, que é a razão de o canal existir.
func TestNtfyDefaultsToThePublicServer(t *testing.T) {
	if _, err := notifier.NewNtfy(json.RawMessage(`{"topic":"upwatch-alertas"}`)); err != nil {
		t.Fatalf("NewNtfy without a server returned %v, want the public default", err)
	}
}

// Tópico com barra ou espaço publicaria noutro caminho do servidor, e o
// celular inscrito no tópico certo não receberia nada.
func TestNtfyRejectsInvalidConfigUpFront(t *testing.T) {
	casos := map[string]string{
		"sem tópico":           `{"server":"https://ntfy.sh"}`,
		"tópico com barra":     `{"topic":"a/b"}`,
		"tópico com espaço":    `{"topic":"meu topico"}`,
		"servidor sem esquema": `{"server":"ntfy.sh","topic":"t"}`,
		"servidor ftp":         `{"server":"ftp://ntfy.sh","topic":"t"}`,
		"json malformado":      `{nope`,
	}
	for nome, cfg := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err == nil {
				t.Fatalf("NewNtfy(%s) returned nil error, want an error", cfg)
			}
		})
	}
}
