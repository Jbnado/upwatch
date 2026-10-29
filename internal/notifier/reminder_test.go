package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/notifier"
)

// reminder monta o lembrete de uma queda aberta há d.
func reminder(d time.Duration) notifier.Notification {
	n := outage()
	n.Reminder = true
	n.Event = incident.Event{
		Kind: incident.KindDown, From: domain.StatusDown, To: domain.StatusDown,
		At: epoch.Add(d), Duration: d,
	}
	return n
}

// O lembrete precisa dizer há quanto tempo, e não repetir "está fora do
// ar" como se fosse a queda chegando agora: quem acorda às oito e vê a
// frase da madrugada repetida não sabe se é a mesma queda ou outra.
func TestRenderReminderSaysHowLong(t *testing.T) {
	texto := notifier.Render(reminder(2*time.Hour + 30*time.Minute))

	for _, quer := range []string{"continua fora do ar há 2 h 30 min", "status 502"} {
		if !strings.Contains(texto, quer) {
			t.Errorf("lembrete = %q, falta %q", texto, quer)
		}
	}
}

// O receptor (um bot que abre chamado, por exemplo) já integra contra o
// envelope. O lembrete chega com os mesmos campos e o mesmo status, e só
// o campo novo diz que é repetição — sem ele, o bot abriria um segundo
// chamado para a mesma queda; com status diferente, quebraria quem só
// olha "status".
func TestWebhookEnvelopeMarksReminder(t *testing.T) {
	cap, url := newCapture(t)
	w, err := notifier.NewWebhook(json.RawMessage(`{"url":"` + url + `"}`))
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}

	if err := send(t, w, outage()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := send(t, w, reminder(time.Hour)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var queda, lembrete map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &queda)
	_ = json.Unmarshal([]byte(cap.body(1)), &lembrete)

	if queda["event"] != "transition" {
		t.Errorf("queda: event = %v, quer transition", queda["event"])
	}
	if lembrete["event"] != "reminder" {
		t.Errorf("lembrete: event = %v, quer reminder", lembrete["event"])
	}
	for _, campo := range []string{"monitor", "monitor_id", "status", "message", "group"} {
		if lembrete[campo] != queda[campo] {
			t.Errorf("campo %s mudou no lembrete: %v → %v", campo, queda[campo], lembrete[campo])
		}
	}
	if lembrete["duration_seconds"] != float64(3600) {
		t.Errorf("duration_seconds = %v, quer 3600 (há quanto tempo dura)", lembrete["duration_seconds"])
	}
	for _, campo := range []string{"text", "previous_status", "at"} {
		if _, ok := lembrete[campo]; !ok {
			t.Errorf("campo %s sumiu do envelope no lembrete", campo)
		}
	}
}

// Quem mapeia o corpo também precisa distinguir, senão o lembrete chega
// idêntico à queda num destino de esquema fixo.
func TestBodyTemplateResolvesEvent(t *testing.T) {
	corpo := entrega(t, `{"tipo":"$event"}`, reminder(time.Hour))
	if corpo["tipo"] != "reminder" {
		t.Errorf("tipo = %v, quer reminder", corpo["tipo"])
	}
}
