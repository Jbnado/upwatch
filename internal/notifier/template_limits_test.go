package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// configComModelo monta a configuração de webhook com o modelo dado.
func configComModelo(url, modelo string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"url": url, "template": modelo})
	return raw
}

// O cadastro ensaia o modelo; sem a conferência da árvore, este laço
// prenderia a requisição por horas.
func TestTemplateWithHugeRangeIsRejectedQuickly(t *testing.T) {
	modelos := []string{
		"{{range 1000000000}}{{range 1000000000}}{{end}}{{end}}",
		"{{range 1000000000}}x{{end}}",
		"{{range .Monitor.ID}}x{{end}}",
		"{{$n := 1000000000}}{{range $n}}x{{end}}",
		"{{range (len .Text)}}x{{end}}",
		"{{range .Monitor.Tags}}{{range $.Monitor.Tags}}x{{end}}{{end}}",
		`{{define "x"}}{{template "x" .}}{{end}}{{template "x" .}}`,
		`{{block "x" .}}y{{end}}`,
	}
	for _, m := range modelos {
		done := make(chan error, 1)
		go func() {
			_, err := notifier.NewWebhook(configComModelo("http://127.0.0.1:1/x", m))
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("template %q was accepted, want it rejected", m)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("validating %q took more than 2s", m)
		}
	}
}

// Percorrer as etiquetas é o uso legítimo do range, e continua aceito.
func TestTemplateMayRangeOverTags(t *testing.T) {
	cap, url := newCapture(t)
	modelos := []string{
		"{{.Monitor.Name}}{{range .Monitor.Tags}} #{{.}}{{end}}",
		"{{range $i, $t := $.Monitor.Tags}}{{$t}}{{else}}sem etiqueta{{end}}",
		"{{with .Monitor}}{{range .Tags}}{{.}}{{end}}{{end}}",
	}
	for _, m := range modelos {
		w, err := notifier.NewWebhook(configComModelo(url, m))
		if err != nil {
			t.Errorf("template %q rejected: %v", m, err)
			continue
		}
		n := outage()
		n.Monitor.Tags = []string{"prod", "rpa"}
		if err := send(t, w, n); err != nil {
			t.Fatalf("Send returned unexpected error: %v", err)
		}
	}
	if !strings.Contains(cap.body(0), `api de produção #prod #rpa`) {
		t.Errorf("payload = %s, want the tags in the text", cap.body(0))
	}
}

func TestTemplateLengthIsCapped(t *testing.T) {
	_, err := notifier.NewWebhook(configComModelo("http://127.0.0.1:1/x", strings.Repeat("a", 9<<10)))
	if err == nil {
		t.Fatal("a 9 KiB template was accepted, want it rejected")
	}
}

// Texto montado sem passar pela saída — numa variável — também é
// limitado: dobrá-lo linha a linha consumiria memória sem fim.
func TestTemplateCannotGrowTextWithoutBound(t *testing.T) {
	modelos := []string{
		`{{$a := "xxxxxxxxxxxxxxxx"}}` + strings.Repeat(`{{$a = print $a $a}}`, 20),
		`{{printf "%01000000d" 1}}`,
		`{{printf "%.9999999s" .Text}}`,
		`{{printf "%*d" 1000000 1}}`,
		// O js dobra cada barra invertida; aplicado sobre si mesmo, cresce
		// em progressão geométrica.
		"{{js " + strings.Repeat("(js ", 39) + `"\\"` + strings.Repeat(")", 39) + "}}",
	}
	for _, m := range modelos {
		if _, err := notifier.NewWebhook(configComModelo("http://127.0.0.1:1/x", m)); err == nil {
			t.Errorf("template %.60q was accepted, want it rejected", m)
		}
	}
}

// Na hora do alerta, a causa de verdade pode ser bem maior que a do
// ensaio. O modelo que passa do limite cai no texto padrão, e o aviso sai.
func TestTemplateOutputOverTheLimitFallsBackToTheDefaultText(t *testing.T) {
	cap, url := newCapture(t)
	w, err := notifier.NewWebhook(configComModelo(url, "{{range .Monitor.Tags}}{{$.Message}}{{end}}"))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}

	n := outage()
	n.Monitor.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	n.Message = strings.Repeat("x", 10<<10)
	if err := send(t, w, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("the payload is not valid JSON: %v", err)
	}
	if want := notifier.Render(n); corpo["text"] != want {
		t.Errorf("text has %d bytes, want the default sentence", len(corpo["text"].(string)))
	}
}

// Os recursos comuns do modelo seguem funcionando com as funções trocadas.
func TestTemplateBuiltinsStillWork(t *testing.T) {
	cap, url := newCapture(t)
	w, err := notifier.NewWebhook(configComModelo(url,
		`{{printf "%s=%5d" .Status 7}}|{{print .Monitor.Name}}|{{urlquery .Group}}|{{html "<b>"}}|{{js "a'b"}}|{{printf "%%"}}`))
	if err != nil {
		t.Fatalf("NewWebhook returned unexpected error: %v", err)
	}
	n := outage()
	n.Group = "RPA / prod"
	if err := send(t, w, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)
	want := `down=    7|api de produção|RPA+%2F+prod|&lt;b&gt;|a\'b|%`
	if corpo["text"] != want {
		t.Errorf("text = %q, want %q", corpo["text"], want)
	}
}
