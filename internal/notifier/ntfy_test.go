package notifier_test

import (
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

// O token vai como Bearer no cabeçalho. Por http, ele atravessa a rede em
// claro, e quem estiver no caminho passa a publicar — e, com permissão de
// leitura, a ler — no tópico. Loopback fica de fora: o ntfy na mesma
// máquina, atrás de proxy, é o arranjo comum e não sai da máquina.
func TestNtfyTokenRequiresHTTPSOutsideLoopback(t *testing.T) {
	recusados := []string{
		`{"server":"http://ntfy.exemplo.com","topic":"t","token":"tk_x"}`,
		`{"server":"http://10.0.0.5:8080","topic":"t","token":"tk_x"}`,
	}
	for _, cfg := range recusados {
		if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err == nil {
			t.Errorf("NewNtfy(%s) accepted a token over plain http", cfg)
		}
	}

	aceitos := []string{
		`{"server":"https://ntfy.exemplo.com","topic":"t","token":"tk_x"}`,
		`{"server":"http://localhost:2586","topic":"t","token":"tk_x"}`,
		`{"server":"http://127.0.0.1:2586","topic":"t","token":"tk_x"}`,
		`{"server":"http://[::1]:2586","topic":"t","token":"tk_x"}`,
		// Sem token, não há segredo no cabeçalho.
		`{"server":"http://ntfy.exemplo.com","topic":"t"}`,
	}
	for _, cfg := range aceitos {
		if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err != nil {
			t.Errorf("NewNtfy(%s) returned %v, want it accepted", cfg, err)
		}
	}
}

// O servidor é concatenado ao tópico para formar a URL. Query ou
// fragmento engoliriam o tópico — a publicação iria para a raiz do
// servidor —, e usuário e senha na URL são credencial guardada fora do
// campo de token, onde a regra do https não alcança.
func TestNtfyRejectsServerWithQueryFragmentOrUserinfo(t *testing.T) {
	casos := map[string]string{
		"query":     `{"server":"https://ntfy.exemplo.com?x=1","topic":"t"}`,
		"fragmento": `{"server":"https://ntfy.exemplo.com#a","topic":"t"}`,
		"userinfo":  `{"server":"https://u:p@ntfy.exemplo.com","topic":"t"}`,
	}
	for nome, cfg := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err == nil {
				t.Fatalf("NewNtfy(%s) returned nil error, want an error", cfg)
			}
		})
	}
}

// No ntfy.sh público o tópico é a única credencial: quem souber o nome lê
// os alertas, que dizem o que caiu e por quê. Um tópico curto como
// "alertas" é adivinhado por qualquer um que tente.
func TestNtfyPublicServerRequiresALongTopic(t *testing.T) {
	recusados := []string{
		`{"topic":"alertas"}`,
		`{"server":"https://ntfy.sh","topic":"upwatch"}`,
		`{"server":"https://NTFY.sh/","topic":"upwatch"}`,
	}
	for _, cfg := range recusados {
		if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err == nil {
			t.Errorf("NewNtfy(%s) accepted a short topic on the public server", cfg)
		}
	}
	if _, err := notifier.NewNtfy(json.RawMessage(`{"topic":"upwatch-x7k2q9"}`)); err != nil {
		t.Errorf("NewNtfy with a 14-char topic returned %v, want it accepted", err)
	}
	// Servidor próprio decide a própria política de nomes.
	if _, err := notifier.NewNtfy(json.RawMessage(`{"server":"https://ntfy.exemplo.com","topic":"t"}`)); err != nil {
		t.Errorf("NewNtfy with a short topic on a private server returned %v, want it accepted", err)
	}
}

// Quebra de linha no token vira cabeçalho novo na requisição — ou, no
// mínimo, uma requisição que o Go recusa montar em todo aviso. Melhor
// recusar no cadastro.
func TestNtfyRejectsTokenWithControlCharacters(t *testing.T) {
	for _, token := range []string{`tk_a\r\nX-Outro: 1`, `tk_a\u0000b`, `tk_a\tb`} {
		cfg := `{"server":"https://ntfy.exemplo.com","topic":"t","token":"` + token + `"}`
		if _, err := notifier.NewNtfy(json.RawMessage(cfg)); err == nil {
			t.Errorf("NewNtfy accepted the token %s", token)
		}
	}
}

// O ntfy transforma em anexo a mensagem acima de 4096 bytes, e no celular
// chega um arquivo em vez do texto. A causa vem do alvo e pode ser uma
// página de erro inteira. O corte respeita o UTF-8: um caractere partido
// ao meio vira lixo no fim da notificação.
func TestNtfyMessageFitsTheNtfyLimit(t *testing.T) {
	cap, url := newCapture(t)
	n := newNtfy(t, `{"server":"`+url+`","topic":"t"}`)

	note := outage()
	note.Message = strings.Repeat("ç", 5000)
	if err := send(t, n, note); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	corpo := cap.body(0)
	if len(corpo) > 4096 {
		t.Errorf("the message has %d bytes, want at most 4096", len(corpo))
	}
	if !utf8.ValidString(corpo) {
		t.Error("the cut split a UTF-8 character")
	}
	if !strings.HasPrefix(corpo, "api de produção está fora do ar") {
		t.Errorf("the message lost its beginning: %q", corpo[:60])
	}
}
