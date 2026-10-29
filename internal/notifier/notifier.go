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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	neturl "net/url"
	"strings"
	"text/template"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
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
	// Group é o caminho do grupo do monitor ("RPA / prod"), vazio sem
	// grupo. Monitores costumam se chamar só "api" ou "front", e é o
	// grupo que diz qual sistema caiu.
	Group string
	// Dependents é quantos monitores ficaram calados por esta queda.
	Dependents int
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
// Build, Types e SecretFields leem daqui: com duas listas, um tipo
// acabaria oferecido na tela e recusado ao salvar, ou aceito pela API e
// com a credencial em texto puro no banco. Um canal novo é uma linha aqui
// e um construtor, e a linha obriga a dizer o que nele é segredo.
var canais = []struct {
	tipo     string
	novo     func(json.RawMessage) (Notifier, error)
	segredos secretbox.Fields
}{
	{"webhook", NewWebhook, webhookSecrets},
	{"discord", NewDiscord, webhookSecrets},
	{"slack", NewSlack, webhookSecrets},
	{"email", NewEmail, secretbox.Fields{Names: []string{"password"}}},
	{"ntfy", NewNtfy, secretbox.Fields{Names: []string{"token"}}},
}

// webhookSecrets vale para os três canais de webhook. A URL do Discord e a
// do Slack levam o token no caminho: quem a tem publica no canal. Os
// cabeçalhos existem para autenticar contra o destino, então todos são
// credencial — ao contrário do monitor HTTP, aqui nenhum volta pela API
// para a interface mostrar.
var webhookSecrets = secretbox.Fields{Names: []string{"url"}, Map: "headers"}

// normalize é como o tipo gravado vira o tipo do registro.
func normalize(kind string) string { return strings.ToLower(strings.TrimSpace(kind)) }

// Build monta o notificador do tipo pedido.
func Build(kind string, config json.RawMessage) (Notifier, error) {
	tipo := normalize(kind)
	for _, c := range canais {
		if c.tipo == tipo {
			return c.novo(config)
		}
	}
	return nil, fmt.Errorf("notifier: tipo de canal desconhecido %q", kind)
}

// SecretFields diz onde a configuração do canal guarda credencial, para
// a cifragem em repouso. Tipo desconhecido não tem o que cifrar: nem
// chega a ser montado.
func SecretFields(kind string) secretbox.Fields {
	tipo := normalize(kind)
	for _, c := range canais {
		if c.tipo == tipo {
			return c.segredos
		}
	}
	return secretbox.Fields{}
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
	// O grupo vem entre parênteses depois do nome, e não antes: o começo
	// da frase continua sendo o monitor, que é o que a notificação do
	// celular mostra cortada. Sem grupo a frase é a de sempre, para não
	// quebrar filtro escrito contra ela.
	if n.Group != "" {
		nome += " (" + n.Group + ")"
	}

	switch n.Event.Kind {
	case domain.ChangeDown:
		texto := fmt.Sprintf("%s está fora do ar", nome)
		if n.Message != "" {
			texto += ": " + n.Message
		}
		return texto + dependentsNote(n.Dependents)

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

// dependentsNote diz quantos monitores a queda deixou sem aviso próprio.
func dependentsNote(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " (1 monitor dependente sem aviso enquanto isso durar)"
	default:
		return fmt.Sprintf(" (%d monitores dependentes sem aviso enquanto isso durar)", n)
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
	Group    string
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
		Group:  n.Group,
		Status: n.Status(), Message: n.Message,
		Duration: humanDuration(n.Event.Duration), At: n.Event.At, Text: texto,
	}
}

// exemploModelo é o evento com que o modelo é ensaiado no cadastro.
var exemploModelo = templateData{
	Monitor: templateMonitor{
		ID: 1, Name: "api", Target: "https://api.exemplo.com", Type: "http", Tags: []string{"producao"},
	},
	Group:  "RPA / prod",
	Status: "down", Message: "status 502", Duration: "5 min",
	At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Text: "api (RPA / prod) está fora do ar: status 502",
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

// ---------- erro permanente ----------

// permanente é o erro que sabe dizer se repetir adianta.
//
// Uma interface, e não uma lista de tipos no despachante: quem entende o
// protocolo é o canal, e é ele que diz se a recusa é definitiva.
type permanente interface{ Permanent() bool }

// permanentError marca um erro como definitivo.
type permanentError struct{ err error }

func (e *permanentError) Error() string   { return e.err.Error() }
func (e *permanentError) Unwrap() error   { return e.err }
func (e *permanentError) Permanent() bool { return true }

// Permanent marca o erro como definitivo: a fila não repete a entrega.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent informa se repetir a entrega não pode mudar o resultado.
//
// Na dúvida, não é: perder um aviso por desistir cedo demais é pior que
// repetir uma recusa. Timeout e erro de rede caem aqui como transitórios.
// Resposta 5xx do SMTP é permanente pelo próprio protocolo (RFC 5321,
// 4.2.1) — o 4xx é o que o servidor usa para pedir que se tente de novo.
func IsPermanent(err error) bool {
	var p permanente
	if errors.As(err, &p) {
		return p.Permanent()
	}
	var smtp *textproto.Error
	if errors.As(err, &smtp) {
		return smtp.Code >= 500 && smtp.Code < 600
	}
	return false
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

// Permanent diz se repetir a entrega pode dar outro resultado.
//
// Só os códigos em que o destino disse, sem ambiguidade, que aquela
// requisição nunca vai passar: corpo malformado (400), credencial recusada
// (401, 403), destino apagado (404), corpo grande demais (413) ou
// recusado pelo conteúdo (422). 429 e 5xx ficam de fora: limite de taxa e
// servidor sobrecarregado são exatamente o que a espera resolve.
func (e *statusError) Permanent() bool {
	switch e.Code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
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
		return fmt.Errorf("notifier: montando requisição: %w", maskURL(err))
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("notifier: entregando aviso: %w", maskURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return &statusError{Code: resp.StatusCode}
	}
	return nil
}

// maskURL tira do erro tudo que vem depois do host.
//
// O erro de rede do Go traz a URL inteira, e ele vai para o log e para a
// resposta do botão de teste. Nos canais, o caminho e a query são a
// credencial: o token do webhook do Discord e do Slack, o tópico do ntfy
// — que no servidor público é a única coisa entre o alerta e qualquer um.
// O host fica, porque sem ele ninguém sabe qual destino caiu. O erro
// continua embrulhado, e timeout segue reconhecível como timeout.
func maskURL(err error) error {
	var ue *neturl.Error
	if !errors.As(err, &ue) {
		return err
	}
	ue.URL = semCredencial(ue.URL)
	return err
}

// semCredencial reduz a URL a esquema e host.
func semCredencial(bruta string) string {
	u, err := neturl.Parse(bruta)
	if err != nil || u.Host == "" {
		return "(url omitida)"
	}
	if u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// requireURL confere que o canal sabe para onde enviar.
func requireURL(url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("notifier: o canal precisa de uma url")
	}
	return nil
}
