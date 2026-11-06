package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	neturl "net/url"
	"regexp"
	"strings"
	"text/template"
)

// ---------- webhook genérico ----------

// webhookConfig é a configuração do canal genérico.
type webhookConfig struct {
	URL string `json:"url"`
	// Headers permite autenticar contra o destino, que costuma exigir
	// token próprio.
	Headers map[string]string `json:"headers,omitempty"`
	// Template substitui o texto padrão.
	Template string `json:"template,omitempty"`
	// BodyTemplate substitui a forma do corpo inteiro, e não só a frase.
	// Existe porque um destino que já existe espera os campos com os nomes
	// dele, e nem sempre dá para mudar quem recebe.
	BodyTemplate json.RawMessage `json:"body_template,omitempty"`
}

type webhook struct {
	cfg  webhookConfig
	tmpl *template.Template
	body *bodyTemplate
}

// NewWebhook cria o canal genérico.
//
// O corpo é um JSON estável com os campos crus além do texto: quem
// integra costuma querer o dado, não a frase.
func NewWebhook(raw json.RawMessage) (Notifier, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de webhook inválida: %w", err)
	}
	if err := requireURL(cfg.URL); err != nil {
		return nil, err
	}

	tmpl, err := compileTemplate(cfg.Template)
	if err != nil {
		return nil, err
	}

	body, err := compileBody(cfg.BodyTemplate)
	if err != nil {
		return nil, err
	}
	return &webhook{cfg: cfg, tmpl: tmpl, body: body}, nil
}

func (w *webhook) Send(ctx context.Context, n Notification) error {
	texto := renderWith(w.tmpl, n)

	// Com mapeamento, a forma é de quem recebe. Sem ele, o envelope de
	// sempre — quem já integrou contra ele não pode quebrar porque o
	// recurso passou a existir.
	if w.body != nil {
		return post(ctx, w.cfg.URL, w.cfg.Headers, w.body.render(dados{n: n, texto: texto}))
	}

	return post(ctx, w.cfg.URL, w.cfg.Headers, map[string]any{
		"text":             texto,
		"monitor":          n.Monitor.Name,
		"monitor_id":       n.Monitor.ID,
		"target":           n.Monitor.Target,
		"status":           n.Status(),
		"previous_status":  n.Event.From.String(),
		"message":          n.Message,
		"at":               n.Event.At,
		"duration_seconds": int(n.Event.Duration.Seconds()),
		// Sempre presente, vazio sem grupo: um campo que some conforme o
		// monitor reprova validação de esquema do outro lado.
		"group": n.Group,
		// Campo novo, e não status diferente: no lembrete o status segue
		// "down", e quem só olha os campos antigos continua lendo uma queda
		// aberta, como de fato é.
		"event": n.Kind(),
	})
}

// cabecalhoSemTLS recusa cabeçalho próprio em destino http.
//
// Os cabeçalhos existem para autenticar contra o destino, e por http
// atravessam a rede em claro: quem estiver no caminho passa a publicar no
// lugar do UpWatch. É a mesma regra do token do ntfy, com a mesma exceção
// do loopback, que não sai da máquina.
func cabecalhoSemTLS(raw json.RawMessage) error {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("notifier: configuração inválida: %w", err)
	}
	if len(cfg.Headers) == 0 {
		return nil
	}
	u, err := neturl.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return fmt.Errorf("notifier: a url do canal não é um endereço válido")
	}
	if strings.EqualFold(u.Scheme, "https") || loopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("notifier: com cabeçalhos, a url do canal precisa de https; " +
		"por http eles atravessariam a rede em claro")
}

// recusarMapeamento barra o mapeamento nos canais de formato fixo.
//
// O corpo do Discord e do Slack é ditado pelo destino: um objeto com a
// forma trocada é recusado do outro lado, e o canal passaria a falhar sem
// que nada tivesse avisado. Recusar no cadastro é mais honesto que aceitar
// uma configuração que não pode funcionar.
func recusarMapeamento(canal string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	return fmt.Errorf("notifier: %s tem corpo próprio e não aceita body_template; "+
		"use o canal webhook para escolher a forma do corpo", canal)
}

// ---------- Discord ----------

type discord struct {
	cfg  webhookConfig
	tmpl *template.Template
}

// NewDiscord cria o canal do Discord.
func NewDiscord(raw json.RawMessage) (Notifier, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de Discord inválida: %w", err)
	}
	if err := requireURL(cfg.URL); err != nil {
		return nil, err
	}
	if err := recusarMapeamento("discord", cfg.BodyTemplate); err != nil {
		return nil, err
	}

	tmpl, err := compileTemplate(cfg.Template)
	if err != nil {
		return nil, err
	}
	return &discord{cfg: cfg, tmpl: tmpl}, nil
}

func (d *discord) Send(ctx context.Context, n Notification) error {
	// O alvo entra como segunda linha em vez de virar campo estruturado:
	// a notificação é lida no celular, e um cartão com metadados exige
	// mais toques para chegar à mesma informação.
	n = discordEscapeOutside(n)
	conteudo := renderWith(d.tmpl, n)
	if n.Monitor.Target != "" {
		conteudo += "\n" + n.Monitor.Target
	}

	return post(ctx, d.cfg.URL, d.cfg.Headers, map[string]any{
		"content": conteudo,
		// Nenhuma menção é interpretada. A causa de um monitor push é
		// escrita por quem bate no UpWatch, e um "@everyone" ali acordaria
		// o servidor inteiro; o Discord só deixa de pingar quando o
		// payload diz que nada é permitido.
		"allowed_mentions": map[string][]string{"parse": {}},
	})
}

// markdownEscaper põe barra invertida antes do que o Markdown do Discord
// e do Teams lê como formatação: ênfase, código, spoiler, citação,
// título, lista e link. Os de começo de linha (#, -, >) são escapados em
// qualquer posição, porque a causa pode trazer quebras de linha e o que
// vem depois de cada uma é começo de linha.
var markdownEscaper = escaperDe(markdownEspeciais)

// markdownEspeciais são os caracteres que o markdownEscaper escapa.
const markdownEspeciais = "\\*_~`|>#-[]()"

// escaperDe põe barra invertida antes de cada caractere dado.
func escaperDe(especiais string) *strings.Replacer {
	pares := make([]string, 0, 2*len(especiais))
	for _, c := range especiais {
		pares = append(pares, string(c), `\`+string(c))
	}
	return strings.NewReplacer(pares...)
}

// palavra é um trecho sem espaço do texto, a unidade em que o Discord
// reconhece um endereço.
var palavra = regexp.MustCompile(`\S+`)

// discordEscape escapa o Markdown de um texto que vem de fora.
//
// O endereço solto fica como está: o Discord o transforma em link pelo
// texto cru, com as barras invertidas dentro, e um alvo como
// https://api-interna.exemplo/saude viraria um link quebrado. Deixá-lo
// intacto não abre brecha — o Discord engole a palavra inteira como um
// link cujo texto é o próprio endereço, e o link disfarçado precisa do
// colchete no começo, que aí é escapado.
func discordEscape(s string) string {
	return palavra.ReplaceAllStringFunc(s, func(p string) string {
		minusc := strings.ToLower(p)
		if strings.HasPrefix(minusc, "https://") || strings.HasPrefix(minusc, "http://") {
			return p
		}
		return markdownEscaper.Replace(p)
	})
}

// discordEscapeOutside escapa o texto que vem de fora antes de montar a
// mensagem, como o Slack faz com o dele.
//
// "[Reautentique aqui](https://outro-lugar)" na causa de um push viraria
// um link disfarçado dentro do alerta, e asteriscos ou um "#" no começo
// da linha mudariam a cara do aviso. O modelo do canal fica de fora: quem
// o escreve pode querer um link para o runbook.
func discordEscapeOutside(n Notification) Notification {
	n.Monitor.Name = discordEscape(n.Monitor.Name)
	n.Monitor.Target = discordEscape(n.Monitor.Target)
	n.Message = discordEscape(n.Message)
	n.Group = discordEscape(n.Group)
	return n
}

// ---------- Slack ----------

type slack struct {
	cfg  webhookConfig
	tmpl *template.Template
}

// NewSlack cria o canal do Slack.
func NewSlack(raw json.RawMessage) (Notifier, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de Slack inválida: %w", err)
	}
	if err := requireURL(cfg.URL); err != nil {
		return nil, err
	}
	if err := recusarMapeamento("slack", cfg.BodyTemplate); err != nil {
		return nil, err
	}

	tmpl, err := compileTemplate(cfg.Template)
	if err != nil {
		return nil, err
	}
	return &slack{cfg: cfg, tmpl: tmpl}, nil
}

func (s *slack) Send(ctx context.Context, n Notification) error {
	n = slackEscapeOutside(n)
	texto := renderWith(s.tmpl, n)
	if n.Monitor.Target != "" {
		texto += "\n" + n.Monitor.Target
	}

	return post(ctx, s.cfg.URL, s.cfg.Headers, map[string]any{
		"text": texto,
	})
}

// slackEscaper escapa o que o Slack lê como controle, conforme a
// documentação de formatação dele: só &, < e >.
var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// slackEscapeOutside escapa o texto que vem de fora antes de montar a
// mensagem.
//
// No Slack, <!channel> é menção em massa e <url|texto> é link disfarçado.
// Nome, grupo, alvo e causa vêm de quem cadastrou o monitor e do próprio alvo — a
// causa de um push é escrita por quem bate no UpWatch. O modelo do canal
// fica de fora de propósito: quem o escreve pode querer um link para o
// runbook, e escapar a mensagem pronta quebraria isso.
func slackEscapeOutside(n Notification) Notification {
	n.Monitor.Name = slackEscaper.Replace(n.Monitor.Name)
	n.Monitor.Target = slackEscaper.Replace(n.Monitor.Target)
	n.Message = slackEscaper.Replace(n.Message)
	n.Group = slackEscaper.Replace(n.Group)
	return n
}
