// Package notifier entrega avisos de mudança de estado.
//
// Um sistema de monitoramento que exige alguém olhando a tela não
// monitora nada. É aqui que a informação sai da ferramenta e chega em
// quem pode agir.
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// sendTimeout limita cada entrega.
//
// Curto de propósito: um canal fora do ar não pode segurar a fila
// enquanto outros incidentes se acumulam atrás dele.
const sendTimeout = 10 * time.Second

// Notification é o que se quer contar.
type Notification struct {
	Monitor domain.Monitor
	Event   domain.StateChange
	// Message é a causa observada, quando houver. É o que decide se
	// alguém precisa levantar da cadeira.
	Message string
}

// Status é o estado que passou a valer, em texto.
func (n Notification) Status() string { return n.Event.To.String() }

// Notifier entrega uma notificação por um canal.
type Notifier interface {
	Send(ctx context.Context, n Notification) error
}

// canais é o registro de tipos atendidos, na ordem em que a interface os
// oferece.
//
// Build e Types leem daqui: com duas listas, um tipo acabaria oferecido na
// tela e recusado ao salvar, ou aceito pela API e inalcançável pela tela.
// Um canal novo é uma linha aqui e um construtor.
var canais = []struct {
	tipo string
	novo func(json.RawMessage) (Notifier, error)
}{
	{"webhook", NewWebhook},
	{"discord", NewDiscord},
	{"slack", NewSlack},
	{"email", NewEmail},
	{"ntfy", NewNtfy},
}

// Build monta o notificador do tipo pedido.
func Build(kind string, config json.RawMessage) (Notifier, error) {
	tipo := strings.ToLower(strings.TrimSpace(kind))
	for _, c := range canais {
		if c.tipo == tipo {
			return c.novo(config)
		}
	}
	return nil, fmt.Errorf("notifier: tipo de canal desconhecido %q", kind)
}

// Types lista os canais atendidos, para a interface montar a escolha.
func Types() []string {
	tipos := make([]string, 0, len(canais))
	for _, c := range canais {
		tipos = append(tipos, c.tipo)
	}
	return tipos
}

// ---------- mensagem ----------

// Render descreve o evento em uma frase.
//
// Escrita para ser lida no celular às três da manhã: o que caiu, o que
// aconteceu e há quanto tempo — nessa ordem, sem preâmbulo.
func Render(n Notification) string {
	nome := n.Monitor.Name

	switch n.Event.Kind {
	case domain.ChangeDown:
		texto := fmt.Sprintf("%s está fora do ar", nome)
		if n.Message != "" {
			texto += ": " + n.Message
		}
		return texto

	case domain.ChangeDegraded:
		texto := fmt.Sprintf("%s está degradado", nome)
		if n.Message != "" {
			texto += ": " + n.Message
		}
		if n.Event.Resolves() {
			texto += fmt.Sprintf(" (esteve fora do ar por %s)", humanDuration(n.Event.Duration))
		}
		return texto

	default:
		if n.Event.Duration > 0 {
			return fmt.Sprintf("%s voltou depois de %s", nome, humanDuration(n.Event.Duration))
		}
		return fmt.Sprintf("%s voltou", nome)
	}
}

// statusLabel é o estado em português, para título e assunto.
//
// Um lugar só: e-mail e ntfy dizendo o mesmo estado com palavras
// diferentes fariam uma regra de caixa de entrada pegar um e não o outro.
func statusLabel(s domain.Status) string {
	switch s {
	case domain.StatusDown:
		return "fora do ar"
	case domain.StatusUp:
		return "no ar"
	case domain.StatusDegraded:
		return "degradado"
	default:
		return "sem medição"
	}
}

// humanDuration escreve a duração como se fala.
//
// "2 h 5 min" em vez de "2h5m0s": a saída padrão do Go serve para log, não
// para a mensagem que alguém lê no celular.
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		horas := int(d.Hours())
		minutos := int(d.Minutes()) - horas*60
		if minutos == 0 {
			return fmt.Sprintf("%d h", horas)
		}
		return fmt.Sprintf("%d h %d min", horas, minutos)
	default:
		dias := int(d.Hours()) / 24
		horas := int(d.Hours()) % 24
		if horas == 0 {
			return fmt.Sprintf("%d d", dias)
		}
		return fmt.Sprintf("%d d %d h", dias, horas)
	}
}

// templateMonitor é o monitor como um modelo de mensagem o enxerga.
//
// Uma cópia mínima, e nunca o domain.Monitor: o modelo é escrito por quem
// cadastra canal, e o monitor carrega em Config a senha do check HTTP e o
// token do push. Com o monitor inteiro ao alcance, um webhook com
// {{printf "%s" .Monitor.Config}} receberia esses segredos no próximo
// aviso. Campo novo aqui é decisão de publicar aquele dado a quem cadastra
// canal.
type templateMonitor struct {
	ID     int64
	Name   string
	Target string
	Type   string
	Tags   []string
}

// templateData é o que um modelo próprio enxerga.
type templateData struct {
	Monitor  templateMonitor
	Status   string
	Message  string
	Duration string
	At       time.Time
	Text     string
}

func newTemplateData(n Notification, texto string) templateData {
	return templateData{
		Monitor: templateMonitor{
			ID: n.Monitor.ID, Name: n.Monitor.Name, Target: n.Monitor.Target,
			Type: n.Monitor.Type.String(), Tags: append([]string(nil), n.Monitor.Tags...),
		},
		Status: n.Status(), Message: n.Message,
		Duration: humanDuration(n.Event.Duration), At: n.Event.At, Text: texto,
	}
}

// exemploModelo é o evento com que o modelo é ensaiado no cadastro.
var exemploModelo = templateData{
	Monitor: templateMonitor{
		ID: 1, Name: "api", Target: "https://api.exemplo.com", Type: "http", Tags: []string{"producao"},
	},
	Status: "down", Message: "status 502", Duration: "5 min",
	At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Text: "api está fora do ar: status 502",
}

// compileTemplate prepara o modelo do canal, se houver.
//
// A compilação acontece no cadastro para o erro aparecer ali, e não
// durante o incidente — que é justamente quando a mensagem não pode
// falhar. O modelo também é ensaiado com um evento de exemplo: um campo
// que não existe, como .Monitor.Config, só aparece na execução, e
// descobri-lo no cadastro diz a quem escreveu que aquilo não está ao
// alcance, em vez de o aviso cair calado no texto padrão.
func compileTemplate(raw string) (*template.Template, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	t, err := template.New("mensagem").Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("notifier: modelo de mensagem inválido: %w", err)
	}
	if err := t.Execute(io.Discard, exemploModelo); err != nil {
		return nil, fmt.Errorf("notifier: modelo de mensagem inválido: %w", err)
	}
	return t, nil
}

// renderWith aplica o modelo próprio, ou devolve o texto padrão.
func renderWith(t *template.Template, n Notification) string {
	padrao := Render(n)
	if t == nil {
		return padrao
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, newTemplateData(n, padrao)); err != nil {
		// Modelo que falha em execução não pode engolir o aviso: o texto
		// padrão vai no lugar, porque avisar de forma feia é melhor que
		// não avisar.
		return padrao
	}
	return buf.String()
}

// ---------- transporte HTTP ----------

// post entrega o corpo em JSON e trata a resposta.
func post(ctx context.Context, url string, headers map[string]string, payload any) error {
	corpo, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notifier: preparando payload: %w", err)
	}
	return postRaw(ctx, url, headers, "application/json", corpo)
}

// statusError é a recusa do destino, com o código para quem quiser
// explicá-la melhor que "respondeu 403".
type statusError struct{ Code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("notifier: o canal respondeu %d", e.Code)
}

// postRaw entrega um corpo já pronto e trata a resposta.
//
// Status de erro vira erro de verdade para a fila poder repetir; engolir
// faria a notificação sumir em silêncio, que é o pior desfecho possível
// num sistema de alerta.
func postRaw(ctx context.Context, url string, headers map[string]string, contentType string, corpo []byte) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(corpo))
	if err != nil {
		return fmt.Errorf("notifier: montando requisição: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("notifier: entregando aviso: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return &statusError{Code: resp.StatusCode}
	}
	return nil
}

// requireURL confere que o canal sabe para onde enviar.
func requireURL(url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("notifier: o canal precisa de uma url")
	}
	return nil
}
