package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// ---------- Microsoft Teams ----------

// O destino é o gatilho "When a Teams webhook request is received" do
// app Workflows (Power Automate), que publica num canal ou num chat. Os
// conectores Incoming Webhook do Office 365 estão sendo desligados pela
// Microsoft, e o Workflows é o caminho que sobra sem licença Premium.

type teams struct {
	cfg  webhookConfig
	tmpl *template.Template
	// cliente é nulo em produção; os testes o trocam para confiar no
	// certificado do servidor falso.
	cliente *http.Client
}

// NewTeams cria o canal do Microsoft Teams.
func NewTeams(raw json.RawMessage) (Notifier, error) {
	return newTeams(raw, nil)
}

func newTeams(raw json.RawMessage, cliente *http.Client) (Notifier, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de Teams inválida: %w", err)
	}
	if err := teamsURL(cfg.URL); err != nil {
		return nil, err
	}
	if err := recusarMapeamento("teams", cfg.BodyTemplate); err != nil {
		return nil, err
	}

	tmpl, err := compileTemplate(cfg.Template)
	if err != nil {
		return nil, err
	}
	if cliente == nil {
		cliente = http.DefaultClient
	}
	return &teams{cfg: cfg, tmpl: tmpl, cliente: cliente}, nil
}

// teamsURL recusa só o engano evidente.
//
// O host não é conferido contra uma lista: a Microsoft já mudou o
// endereço do Workflows de *.logic.azure.com para
// *.environment.api.powerplatform.com, e nuvens de governo usam outros
// domínios. Uma lista fechada recusaria o próximo endereço válido. O que
// se barra é o que nunca funciona: http, que o Workflows não atende, e a
// URL de outro canal colada no tipo errado.
func teamsURL(bruta string) error {
	if err := requireURL(bruta); err != nil {
		return err
	}
	u, err := neturl.Parse(strings.TrimSpace(bruta))
	if err != nil || u.Host == "" {
		return fmt.Errorf("notifier: a url do Teams não é um endereço válido")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("notifier: a url do Teams precisa começar com https://")
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "hooks.slack.com":
		return fmt.Errorf("notifier: esta url é de um webhook do Slack; use o tipo slack")
	case host == "discord.com" || host == "discordapp.com" || strings.HasSuffix(host, ".discord.com"):
		return fmt.Errorf("notifier: esta url é de um webhook do Discord; use o tipo discord")
	}
	return nil
}

// urlPublica é o endereço externo da instalação, para o botão do cartão.
var urlPublica atomic.Pointer[string]

// SetPublicURL informa o endereço externo da instalação, como
// "https://status.exemplo.com". Vazio tira o botão "Abrir no UpWatch" do
// cartão do Teams: um link relativo não abre nada fora do navegador.
func SetPublicURL(base string) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	urlPublica.Store(&base)
}

// linkMonitor é a página do monitor na interface, ou o painel quando não
// há monitor — o aviso de teste —, ou vazio sem endereço externo.
func linkMonitor(id int64) string {
	p := urlPublica.Load()
	if p == nil || *p == "" {
		return ""
	}
	if id <= 0 {
		return *p + "/"
	}
	return *p + "/monitors/" + strconv.FormatInt(id, 10)
}

func (t *teams) Send(ctx context.Context, n Notification) error {
	corpo, err := json.Marshal(teamsCard(n, renderWith(t.tmpl, n)))
	if err != nil {
		return fmt.Errorf("notifier: preparando payload: %w", err)
	}
	return postRawVia(ctx, t.cliente, t.cfg.URL, t.cfg.Headers, "application/json", corpo)
}

// teamsCard monta a mensagem com um Adaptive Card.
//
// Versão 1.4 e só elementos que o Teams desenha: o Workflows recusa ou
// mostra em branco o cartão com recurso que ele não conhece, e o aviso
// some sem erro do lado de cá.
func teamsCard(n Notification, frase string) map[string]any {
	estilo, cor, rotulo := teamsEstado(n.Event)

	titulo := n.Monitor.Name
	if n.Group != "" {
		titulo = n.Group + " · " + n.Monitor.Name
	}

	corpo := []any{
		map[string]any{
			"type": "Container", "style": estilo, "bleed": true,
			"items": []any{
				map[string]any{
					"type": "TextBlock", "text": rotulo, "weight": "Bolder",
					"color": cor, "size": "Small", "wrap": true,
				},
				map[string]any{
					"type": "TextBlock", "text": titulo, "weight": "Bolder",
					"size": "Large", "wrap": true, "spacing": "None",
				},
			},
		},
		map[string]any{"type": "TextBlock", "text": frase, "wrap": true},
	}

	fatos := []any{}
	if n.Message != "" {
		fatos = append(fatos, map[string]any{"title": "Causa", "value": n.Message})
	}
	if n.Event.Duration > 0 {
		fatos = append(fatos, map[string]any{"title": "Duração", "value": humanDuration(n.Event.Duration)})
	}
	if n.Monitor.Target != "" {
		fatos = append(fatos, map[string]any{"title": "Alvo", "value": n.Monitor.Target})
	}
	if len(fatos) > 0 {
		corpo = append(corpo, map[string]any{"type": "FactSet", "facts": fatos})
	}

	// DATE e TIME fazem o Teams mostrar o horário no fuso de quem lê: o
	// plantão pode estar em outro fuso que o servidor.
	at := n.Event.At.UTC().Format(time.RFC3339)
	corpo = append(corpo, map[string]any{
		"type": "TextBlock", "text": "{{DATE(" + at + ", SHORT)}} {{TIME(" + at + ")}}",
		"isSubtle": true, "size": "Small", "wrap": true,
	})

	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    corpo,
		// Sem isto o Teams estreita o cartão a uma coluna no computador.
		"msteams": map[string]any{"width": "Full"},
	}
	if link := linkMonitor(n.Monitor.ID); link != "" {
		card["actions"] = []any{
			map[string]any{"type": "Action.OpenUrl", "title": "Abrir no UpWatch", "url": link},
		}
	}

	return map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content":     card,
			},
		},
	}
}

// teamsEstado é o estilo do cabeçalho, a cor e o rótulo do estado.
func teamsEstado(e domain.StateChange) (estilo, cor, rotulo string) {
	switch e.Kind {
	case domain.ChangeDown:
		return "attention", "Attention", "FORA DO AR"
	case domain.ChangeDegraded:
		return "warning", "Warning", "DEGRADADO"
	default:
		return "good", "Good", "VOLTOU"
	}
}
