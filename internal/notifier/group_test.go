package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// O grupo no aviso.
//
// Monitores costumam se chamar só "api" ou "front" dentro de grupos como
// "RPA / prod" e "Retaguarda / prod". Um aviso de "api está fora do ar"
// não diz qual api caiu, e quem recebe precisa abrir o painel para saber
// se levanta da cadeira.

func comGrupo(n notifier.Notification) notifier.Notification {
	n.Group = "RPA / prod"
	return n
}

func TestEnvelopePadraoLevaOGrupo(t *testing.T) {
	corpo := entrega(t, "", comGrupo(outage()))

	if corpo["group"] != "RPA / prod" {
		t.Errorf("group = %v, want o caminho do grupo", corpo["group"])
	}
}

// Sem grupo o campo continua presente e vazio: quem valida esquema do
// outro lado não pode ver o campo sumir conforme o monitor.
func TestEnvelopeSemGrupoTrazCampoVazio(t *testing.T) {
	corpo := entrega(t, "", outage())

	v, ok := corpo["group"]
	if !ok || v != "" {
		t.Errorf("group = %v (presente: %v), want presente e vazio", v, ok)
	}
}

func TestMarcadorGroupChegaNoMapeamento(t *testing.T) {
	corpo := entrega(t, `{"sistema":"$group","resumo":"[$group] $monitor"}`, comGrupo(outage()))

	if corpo["sistema"] != "RPA / prod" {
		t.Errorf("sistema = %v, want o caminho do grupo", corpo["sistema"])
	}
	if corpo["resumo"] != "[RPA / prod] api de produção" {
		t.Errorf("resumo = %v, want o grupo interpolado", corpo["resumo"])
	}
}

func TestRenderNomeiaOGrupoNaQueda(t *testing.T) {
	texto := notifier.Render(comGrupo(outage()))

	want := "api de produção (RPA / prod) está fora do ar: status 502 fora do esperado"
	if texto != want {
		t.Errorf("Render = %q, want %q", texto, want)
	}
}

func TestRenderNomeiaOGrupoNaVolta(t *testing.T) {
	texto := notifier.Render(comGrupo(recovery(3 * time.Minute)))

	want := "api de produção (RPA / prod) voltou depois de 3 min"
	if texto != want {
		t.Errorf("Render = %q, want %q", texto, want)
	}
}

// Sem grupo a frase é a de sempre: regra de caixa de entrada e filtro
// escritos contra ela não podem quebrar porque o recurso passou a existir.
func TestRenderSemGrupoNaoMuda(t *testing.T) {
	texto := notifier.Render(outage())

	if strings.Contains(texto, "(") {
		t.Errorf("Render = %q, want a frase sem grupo", texto)
	}
}

func TestModeloEnxergaOGrupo(t *testing.T) {
	cap, url := newCapture(t)
	w, err := novoWebhookComTexto(url, "", "{{.Group}} · {{.Monitor.Name}}")
	if err != nil {
		t.Fatalf("o cadastro recusou o modelo com .Group: %v", err)
	}
	if err := send(t, w, comGrupo(outage())); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	if corpo["text"] != "RPA / prod · api de produção" {
		t.Errorf("text = %v, want o grupo no modelo", corpo["text"])
	}
}

// O nome do grupo é texto livre de quem cadastra, como o do monitor, e
// no Slack passa pelo mesmo escape.
func TestSlackEscapaONomeDoGrupo(t *testing.T) {
	cap, url := newCapture(t)
	s, err := notifier.NewSlack(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewSlack returned unexpected error: %v", err)
	}

	n := outage()
	n.Group = "RPA <!channel>"
	if err := send(t, s, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	texto, _ := corpo["text"].(string)
	if strings.Contains(texto, "<!channel>") || !strings.Contains(texto, "&lt;!channel&gt;") {
		t.Errorf("text = %q, want o grupo escapado", texto)
	}
}
