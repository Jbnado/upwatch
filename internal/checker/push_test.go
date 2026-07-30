package checker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
)

var pushNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// pushLog devolve o instante do último sinal, controlado pelo teste.
type pushLog struct {
	at  time.Time
	has bool
	err error
}

func (p *pushLog) LastPush(context.Context, int64) (time.Time, bool, error) {
	return p.at, p.has, p.err
}

func pushMonitor(t *testing.T, interval time.Duration, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage(`{"token":"segredo-de-16-chars"}`)
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}
	return domain.Monitor{
		ID: 1, Name: "cron noturno", Type: domain.MonitorPush,
		Interval: interval, Timeout: interval / 2,
		ConfirmationThreshold: 1, Config: raw,
	}
}

func newPush(log *pushLog) *checker.Push {
	return checker.NewPush(log, clock.NewFake(pushNow))
}

func TestPushTypeIsPush(t *testing.T) {
	if got := newPush(&pushLog{}).Type(); got != domain.MonitorPush {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorPush)
	}
}

func TestPushWithinWindowIsUp(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-30 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// A janela é o intervalo mais a folga: um cron que atrasa alguns segundos
// não pode disparar alerta, senão o monitor vira fonte de ruído.
func TestPushToleratesDelayWithinGracePeriod(t *testing.T) {
	// Intervalo de 60s com folga padrão de mais 60s: 90s de silêncio ainda
	// está dentro.
	log := &pushLog{at: pushNow.Add(-90 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestPushBeyondWindowIsDown(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-5 * time.Minute), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "sem sinal") {
		t.Errorf("Message = %q, want it to say no signal arrived", got.Message)
	}
}

func TestPushHonoursExplicitGracePeriod(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-100 * time.Second), has: true}
	cfg := map[string]any{"token": "segredo-de-16-chars", "grace_period_seconds": 300}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, cfg))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Monitor recém-criado ainda não recebeu nada. Marcar como fora do ar
// dispararia alerta antes de o serviço ter tido chance de reportar.
func TestPushNeverSignalledIsUnknown(t *testing.T) {
	got := newPush(&pushLog{has: false}).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUnknown {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUnknown)
	}
}

func TestPushStoreErrorIsDown(t *testing.T) {
	log := &pushLog{err: errors.New("banco indisponível")}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// O tempo desde o último sinal é o que o operador olha para decidir se um
// cron está apenas atrasado ou realmente parado.
func TestPushReportsSecondsSinceLastSignal(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-45 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Meta["seconds_since_push"] != "45" {
		t.Errorf("Meta[seconds_since_push] = %q, want %q", got.Meta["seconds_since_push"], "45")
	}
	if got.Meta["last_push"] == "" {
		t.Error("Meta[last_push] is empty, want the instant of the last signal")
	}
}

// A latência de um monitor push não descreve tempo de resposta de nada:
// preenchê-la poluiria os percentis com um número sem significado.
func TestPushReportsNoLatency(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-10 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0: a push monitor measures no round trip", got.LatencyMS)
	}
}

func TestPushValidateConfigRequiresToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig without a token returned nil error, want an error")
	}
}

// Token curto é adivinhável, e quem adivinha consegue manter um monitor
// artificialmente saudável enquanto o serviço real está parado.
func TestPushValidateConfigRejectsShortToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"token": "curto"})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a short token returned nil error, want an error")
	}
}

func TestPushValidateConfigAcceptsProperToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"token": "um-token-suficientemente-longo"})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig returned unexpected error: %v", err)
	}
}

func TestPushValidateConfigRejectsNegativeGrace(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{
		"token": "um-token-suficientemente-longo", "grace_period_seconds": -1,
	})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a negative grace period returned nil error, want an error")
	}
}

func TestPushValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := newPush(&pushLog{}).ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

func TestPushGenerateTokenProducesDistinctSecrets(t *testing.T) {
	first, err := checker.GeneratePushToken()
	if err != nil {
		t.Fatalf("GeneratePushToken returned unexpected error: %v", err)
	}
	second, err := checker.GeneratePushToken()
	if err != nil {
		t.Fatalf("GeneratePushToken returned unexpected error: %v", err)
	}

	if first == second {
		t.Error("GeneratePushToken returned the same value twice, want unpredictable secrets")
	}
	if len(first) < checker.MinPushTokenLength {
		t.Errorf("generated token has %d characters, want at least %d",
			len(first), checker.MinPushTokenLength)
	}
}
