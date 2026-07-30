package notifier

import (
	"context"
	"encoding/json"
	"fmt"
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
	})
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
	conteudo := renderWith(d.tmpl, n)
	if n.Monitor.Target != "" {
		conteudo += "\n" + n.Monitor.Target
	}

	return post(ctx, d.cfg.URL, d.cfg.Headers, map[string]any{
		"content": conteudo,
	})
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
	texto := renderWith(s.tmpl, n)
	if n.Monitor.Target != "" {
		texto += "\n" + n.Monitor.Target
	}

	return post(ctx, s.cfg.URL, s.cfg.Headers, map[string]any{
		"text": texto,
	})
}
