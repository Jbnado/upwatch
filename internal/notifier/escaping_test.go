package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// Um push com causa "[Reautentique aqui](...)" não pode virar link
// disfarçado no alerta do Discord.
func TestDiscordEscapesMarkdownFromOutside(t *testing.T) {
	cap, url := newCapture(t)
	d, err := notifier.NewDiscord(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewDiscord returned unexpected error: %v", err)
	}

	n := outage()
	n.Monitor.Name = "*api*_prod_"
	n.Group = "~~RPA~~ | prod"
	n.Monitor.Target = "tcp `db-1`:5432"
	n.Message = "[Reautentique aqui](https://evil.example/login)\n# urgente\n> citação\n- item @everyone"
	if err := send(t, d, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo struct {
		Content         string              `json:"content"`
		AllowedMentions map[string][]string `json:"allowed_mentions"`
	}
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}

	want := `\*api\*\_prod\_ (\~\~RPA\~\~ \| prod) está fora do ar: ` +
		`\[Reautentique aqui\]\(https://evil.example/login\)` + "\n" +
		`\# urgente` + "\n" + `\> citação` + "\n" + `\- item @everyone` + "\n" +
		"tcp \\`db\\-1\\`:5432"
	if corpo.Content != want {
		t.Errorf("content =\n%s\nwant\n%s", corpo.Content, want)
	}
	if got, ok := corpo.AllowedMentions["parse"]; !ok || len(got) != 0 {
		t.Errorf("allowed_mentions = %v, want parse empty", corpo.AllowedMentions)
	}
}

// O Discord faz link do endereço solto pelo texto cru: escapá-lo quebraria
// o link do alvo, e não abre brecha — o texto do link é o próprio
// endereço.
func TestDiscordLeavesBareURLsAlone(t *testing.T) {
	cap, url := newCapture(t)
	d, _ := notifier.NewDiscord(json.RawMessage(`{"url":"` + url + `"}`))

	n := outage()
	n.Monitor.Target = "https://api-interna.exemplo/saude_check"
	n.Message = "GET https://api-interna.exemplo/saude_check: timeout"
	if err := send(t, d, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	want := "api de produção está fora do ar: GET https://api-interna.exemplo/saude_check: timeout\n" +
		"https://api-interna.exemplo/saude_check"
	if corpo.Content != want {
		t.Errorf("content = %q, want %q", corpo.Content, want)
	}
}

// O que quem cadastrou o canal escreve no modelo passa intacto, como no
// Slack: um link para o runbook é intencional.
func TestDiscordKeepsTheChannelTemplateIntact(t *testing.T) {
	cap, url := newCapture(t)
	d, err := notifier.NewDiscord(json.RawMessage(`{"url":"` + url +
		`","template":"**{{.Monitor.Name}}** caiu: {{.Message}} — [runbook](https://wiki.exemplo/rb)"}`))
	if err != nil {
		t.Fatalf("NewDiscord returned unexpected error: %v", err)
	}

	n := outage()
	n.Monitor.Target = ""
	n.Message = "[x](https://evil.example)"
	if err := send(t, d, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	want := `**api de produção** caiu: \[x\]\(https://evil.example\) — [runbook](https://wiki.exemplo/rb)`
	if corpo.Content != want {
		t.Errorf("content = %q, want %q", corpo.Content, want)
	}
}

// A causa com link disfarçado chega ao Teams como texto, com a forma
// exata do cartão.
func TestTeamsEscapesMarkdownFromOutside(t *testing.T) {
	canal, ultimo := teamsCapture(t, `{"url":"$URL"}`)

	n := outage()
	n.Monitor.Name = "api_prod"
	n.Group = "RPA - **prod**"
	n.Monitor.Target = "https://api-interna.exemplo/saude"
	n.Message = "[clique aqui](https://outro-lugar.exemplo)"
	if err := send(t, canal, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	card := cartaoTeams(t, ultimo())
	corpo := card["body"].([]any)

	cabecalho := corpo[0].(map[string]any)["items"].([]any)
	if got := cabecalho[1].(map[string]any)["text"]; got != `RPA \- \*\*prod\*\* · api\_prod` {
		t.Errorf("title = %q", got)
	}

	frase := corpo[1].(map[string]any)["text"]
	wantFrase := `api\_prod (RPA \- \*\*prod\*\*) está fora do ar: \[clique aqui\]\(https://outro\-lugar.exemplo\)`
	if frase != wantFrase {
		t.Errorf("sentence = %q, want %q", frase, wantFrase)
	}

	fatos := corpo[2].(map[string]any)["facts"].([]any)
	causa := fatos[0].(map[string]any)
	if causa["title"] != "Causa" || causa["value"] != `\[clique aqui\]\(https://outro\-lugar.exemplo\)` {
		t.Errorf("cause fact = %v", causa)
	}
	alvo := fatos[1].(map[string]any)
	if alvo["value"] != `https://api\-interna.exemplo/saude` {
		t.Errorf("target fact = %v", alvo)
	}
}

// {{DATE(...)}} e {{TIME(...)}} vindos de fora não são avaliados pelo
// Teams; os que o próprio cartão emite continuam funcionando.
func TestTeamsNeutralizesDateFunctionsFromOutside(t *testing.T) {
	canal, ultimo := teamsCapture(t, `{"url":"$URL"}`)

	n := outage()
	n.Message = "{{DATE(2017-02-14T06:08:39Z, SHORT)}} {{TIME(2017-02-14T06:08:39Z)}}"
	if err := send(t, canal, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	bruto := ultimo()
	card := cartaoTeams(t, bruto)
	corpo := card["body"].([]any)

	fatos := corpo[2].(map[string]any)["facts"].([]any)
	causa := fatos[0].(map[string]any)["value"].(string)
	if strings.Contains(causa, "{{") || strings.Contains(causa, "}}") {
		t.Errorf("cause keeps a function the card would evaluate: %q", causa)
	}
	if want := `\{\{DATE\(2017\-02\-14T06:08:39Z, SHORT\)\}\} \{\{TIME\(2017\-02\-14T06:08:39Z\)\}\}`; causa != want {
		t.Errorf("cause = %q, want %q", causa, want)
	}

	rodape := corpo[len(corpo)-1].(map[string]any)["text"]
	if rodape != "{{DATE(2026-07-28T12:00:00Z, SHORT)}} {{TIME(2026-07-28T12:00:00Z)}}" {
		t.Errorf("the card's own timestamp changed: %q", rodape)
	}
}

// O modelo do canal passa intacto no Teams, inclusive um link escrito
// por quem o cadastrou.
func TestTeamsKeepsTheChannelTemplateIntact(t *testing.T) {
	canal, ultimo := teamsCapture(t,
		`{"url":"$URL","template":"[runbook](https://wiki.exemplo/rb) {{.Message}}"}`)

	n := outage()
	n.Message = "**urgente**"
	if err := send(t, canal, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	corpo := cartaoTeams(t, ultimo())["body"].([]any)
	if got := corpo[1].(map[string]any)["text"]; got != `[runbook](https://wiki.exemplo/rb) \*\*urgente\*\*` {
		t.Errorf("sentence = %q", got)
	}
}

// cartaoTeams tira o Adaptive Card da mensagem enviada.
func cartaoTeams(t *testing.T, bruto string) map[string]any {
	t.Helper()
	var msg struct {
		Attachments []struct {
			Content map[string]any `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(bruto), &msg); err != nil || len(msg.Attachments) != 1 {
		t.Fatalf("unexpected Teams payload (%v): %s", err, bruto)
	}
	return msg.Attachments[0].Content
}
