package notifier_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestBuildResolvesEmailAndNtfy(t *testing.T) {
	casos := map[string]string{
		"email": `{"host":"smtp.exemplo.com","from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		"ntfy":  `{"topic":"upwatch-alertas"}`,
	}
	for tipo, cfg := range casos {
		t.Run(tipo, func(t *testing.T) {
			n, err := notifier.Build(tipo, json.RawMessage(cfg))
			if err != nil {
				t.Fatalf("Build(%q) returned unexpected error: %v", tipo, err)
			}
			if n == nil {
				t.Fatalf("Build(%q) returned nil", tipo)
			}
		})
	}
}

// A interface monta a escolha a partir de Types, e o cadastro passa por
// Build. Um tipo listado que Build não conhece seria oferecido na tela e
// recusado ao salvar; um que Build conhece e Types omite ficaria
// inalcançável pela interface. Os dois saem da mesma lista.
func TestTypesAndBuildAgree(t *testing.T) {
	tipos := notifier.Types()
	for _, esperado := range []string{"webhook", "discord", "slack", "email", "ntfy"} {
		if !contem(tipos, esperado) {
			t.Errorf("Types() = %v, missing %q", tipos, esperado)
		}
	}

	for _, tipo := range tipos {
		_, err := notifier.Build(tipo, json.RawMessage(`{}`))
		if err != nil && strings.Contains(err.Error(), "desconhecido") {
			t.Errorf("Types() lists %q but Build does not know it: %v", tipo, err)
		}
	}
}

func contem(lista []string, alvo string) bool {
	for _, v := range lista {
		if v == alvo {
			return true
		}
	}
	return false
}

// O modelo de mensagem é escrito por quem cadastra canal, que não é
// necessariamente quem cadastrou o monitor. Se o modelo enxergasse a
// configuração do monitor, um webhook com {{printf "%s" .Monitor.Config}}
// receberia no próximo aviso a senha do check HTTP e o token do push.
// Recusar no cadastro é o melhor desfecho: o erro aparece na hora, e não
// fica um canal que parece funcionar.
func TestTemplateCannotReadMonitorConfig(t *testing.T) {
	for _, tipo := range []string{"webhook", "discord", "slack"} {
		t.Run(tipo, func(t *testing.T) {
			cfg := `{"url":"http://exemplo.invalido","template":"{{printf \"%s\" .Monitor.Config}}"}`
			if _, err := notifier.Build(tipo, json.RawMessage(cfg)); err == nil {
				t.Fatalf("Build(%s) accepted a template that reads the monitor config", tipo)
			}
		})
	}
}

// O cadastro só prova o caminho que o exemplo percorre; um ramo que só
// executa num estado raro escapa dele. O que impede o vazamento é o modelo
// não receber a configuração de jeito nenhum — este teste prova que,
// mesmo escondido num ramo, o segredo não sai.
func TestTemplateNeverEmitsMonitorSecrets(t *testing.T) {
	const segredo = "s3gr3d0-do-monitor"
	modelos := []string{
		`{{if eq .Status \"down\"}}{{printf \"%s\" .Monitor.Config}}{{end}}`,
		`{{if eq .Status \"down\"}}{{printf \"%v\" .Monitor}}{{end}}`,
		`{{if eq .Status \"down\"}}{{printf \"%+v\" .}}{{end}}`,
	}
	for _, modelo := range modelos {
		t.Run(modelo, func(t *testing.T) {
			cap, url := newCapture(t)
			w, err := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `","template":"` + modelo + `"}`))
			if err != nil {
				// Recusado no cadastro também protege.
				return
			}

			n := outage()
			n.Monitor.Config = json.RawMessage(`{"password":"` + segredo + `","push_token":"` + segredo + `"}`)
			if err := send(t, w, n); err != nil {
				t.Fatalf("Send returned unexpected error: %v", err)
			}
			// %v imprime json.RawMessage como lista de bytes; o segredo
			// continua lá, só que em decimal.
			emBytes := make([]string, len(segredo))
			for i := range segredo {
				emBytes[i] = strconv.Itoa(int(segredo[i]))
			}
			if corpo := cap.body(0); strings.Contains(corpo, segredo) || strings.Contains(corpo, strings.Join(emBytes, " ")) {
				t.Errorf("the custom template leaked the monitor config: %s", corpo)
			}
		})
	}
}

// Recusa que repetir não muda — URL apagada, token revogado, corpo que o
// destino não aceita — precisa parar na primeira tentativa: repetir só
// suja o log e prende um trabalhador da fila enquanto outros incidentes
// esperam. Erro do servidor e limite de taxa, ao contrário, passam.
func TestHTTPRefusalsArePermanentOnlyWhenRetryCannotHelp(t *testing.T) {
	casos := map[int]bool{
		http.StatusBadRequest:            true,
		http.StatusUnauthorized:          true,
		http.StatusForbidden:             true,
		http.StatusNotFound:              true,
		http.StatusRequestEntityTooLarge: true,
		http.StatusUnprocessableEntity:   true,
		http.StatusTooManyRequests:       false,
		http.StatusInternalServerError:   false,
		http.StatusBadGateway:            false,
		http.StatusServiceUnavailable:    false,
	}
	for _, tipo := range []string{"webhook", "discord", "slack", "ntfy"} {
		for codigo, permanente := range casos {
			t.Run(fmt.Sprintf("%s/%d", tipo, codigo), func(t *testing.T) {
				cap, url := newCapture(t)
				cap.status.Store(int32(codigo))

				cfg := `{"url":"` + url + `"}`
				if tipo == "ntfy" {
					cfg = `{"server":"` + url + `","topic":"t"}`
				}
				n, err := notifier.Build(tipo, json.RawMessage(cfg))
				if err != nil {
					t.Fatalf("Build returned unexpected error: %v", err)
				}

				err = send(t, n, outage())
				if err == nil {
					t.Fatalf("Send returned nil error for %d, want an error", codigo)
				}
				if got := notifier.IsPermanent(err); got != permanente {
					t.Errorf("IsPermanent(%v) = %v, want %v", err, got, permanente)
				}
			})
		}
	}
}

// Destino que não responde é falha de rede, a mais momentânea de todas.
func TestNetworkFailureIsTransient(t *testing.T) {
	url := closedURL(t)
	w, err := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `/x"}`))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}

	err = send(t, w, outage())
	if err == nil {
		t.Fatal("Send to a closed port returned nil error, want an error")
	}
	if notifier.IsPermanent(err) {
		t.Errorf("IsPermanent(%v) = true, want a network failure to be retried", err)
	}
}

// closedURL devolve o endereço de uma porta que acabou de ser fechada.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

// A causa de um monitor push é escrita por quem bate no UpWatch, e o nome
// do monitor por quem o cadastrou. Um "@everyone" ali acordaria o
// servidor inteiro do Discord às três da manhã. O Discord só deixa de
// interpretar menção quando o payload diz que nenhuma é permitida.
func TestDiscordDisablesMassMentions(t *testing.T) {
	cap, url := newCapture(t)
	d, err := notifier.NewDiscord(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewDiscord returned unexpected error: %v", err)
	}

	n := outage()
	n.Message = "@everyone @here <@&123> caiu"
	if err := send(t, d, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo struct {
		Content         string `json:"content"`
		AllowedMentions *struct {
			Parse *[]string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	if corpo.AllowedMentions == nil || corpo.AllowedMentions.Parse == nil || len(*corpo.AllowedMentions.Parse) != 0 {
		t.Errorf("allowed_mentions = %s, want {\"parse\": []} so no mention pings", cap.body(0))
	}
	if !strings.Contains(corpo.Content, "@everyone") {
		t.Errorf("content = %q, want the cause kept as text", corpo.Content)
	}
}

// No Slack a menção em massa é <!channel>, <!here> ou <!everyone>, e o
// link disfarçado é <url|texto>. Texto vindo do alvo precisa ter <, > e &
// escapados, como a documentação de formatação do Slack pede, para ser
// lido como texto e não como comando.
func TestSlackEscapesControlSequencesFromOutside(t *testing.T) {
	cap, url := newCapture(t)
	s, err := notifier.NewSlack(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewSlack returned unexpected error: %v", err)
	}

	n := outage()
	n.Monitor.Name = "api <!here>"
	n.Message = "@everyone <!channel> <https://phishing.exemplo|clique aqui> a & b"
	if err := send(t, s, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	texto, _ := corpo["text"].(string)

	for _, cru := range []string{"<!channel>", "<!here>", "<https://phishing"} {
		if strings.Contains(texto, cru) {
			t.Errorf("text carries raw %q, want it escaped: %q", cru, texto)
		}
	}
	for _, escapado := range []string{"&lt;!channel&gt;", "a &amp; b"} {
		if !strings.Contains(texto, escapado) {
			t.Errorf("text = %q, want it to contain %q", texto, escapado)
		}
	}
}

// O modelo é de quem cadastra o canal, que pode querer formatação do
// Slack de propósito — um link para o runbook. Escapar o modelo inteiro
// quebraria isso; só o que vem de fora é escapado.
func TestSlackKeepsFormattingWrittenInTheTemplate(t *testing.T) {
	cap, url := newCapture(t)
	cfg := `{"url":"` + url + `","template":"<https://runbook.exemplo|runbook> {{.Message}}"}`
	s, err := notifier.NewSlack(json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("NewSlack returned unexpected error: %v", err)
	}

	n := outage()
	n.Message = "<!channel>"
	if err := send(t, s, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	texto, _ := corpo["text"].(string)
	if !strings.HasPrefix(texto, "<https://runbook.exemplo|runbook> &lt;!channel&gt;") {
		t.Errorf("text = %q, want the template link kept and the cause escaped", texto)
	}
}

// O erro de entrega vai para o log e para a resposta do botão de teste. O
// erro de rede do Go carrega a URL inteira, e nela está a credencial: o
// token no caminho do webhook do Discord e do Slack, e o tópico do ntfy —
// que no servidor público é a única coisa entre o alerta e qualquer um.
// Log é lido por mais gente, e guardado por mais tempo, que a
// configuração do canal.
func TestDeliveryErrorsDoNotCarryTheSecretPartOfTheURL(t *testing.T) {
	base := closedURL(t)
	const segredo = "tokenSecretoXYZ123"

	casos := map[string]string{
		"webhook": `{"url":"` + base + `/hooks/` + segredo + `?chave=` + segredo + `"}`,
		"discord": `{"url":"` + base + `/api/webhooks/123/` + segredo + `"}`,
		"slack":   `{"url":"` + base + `/services/T000/B000/` + segredo + `"}`,
		"ntfy":    `{"server":"` + base + `","topic":"` + segredo + `"}`,
	}
	for tipo, cfg := range casos {
		t.Run(tipo, func(t *testing.T) {
			n, err := notifier.Build(tipo, json.RawMessage(cfg))
			if err != nil {
				t.Fatalf("Build returned unexpected error: %v", err)
			}

			err = send(t, n, outage())
			if err == nil {
				t.Fatal("Send to a closed port returned nil error, want an error")
			}
			if strings.Contains(err.Error(), segredo) {
				t.Errorf("error = %q leaks the secret part of the URL", err)
			}
			// O host continua: sem ele, ninguém sabe qual destino caiu.
			if !strings.Contains(err.Error(), "127.0.0.1") {
				t.Errorf("error = %q, want it to keep the host", err)
			}
		})
	}
}
