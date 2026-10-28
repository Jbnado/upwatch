package domain_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Sinal sem status continua significando "estou vivo": quem já integrou
// o push antes de o parâmetro existir não pode passar a ser lido como
// falha, nem precisar mudar o script.
func TestParsePushStatusDefaultsToUp(t *testing.T) {
	got, err := domain.ParsePushStatus("")
	if err != nil {
		t.Fatalf("ParsePushStatus(\"\") returned unexpected error: %v", err)
	}
	if got != domain.StatusUp {
		t.Errorf("ParsePushStatus(\"\") = %v, want %v", got, domain.StatusUp)
	}
}

func TestParsePushStatusAcceptsReportableStatuses(t *testing.T) {
	cases := map[string]domain.Status{
		"up": domain.StatusUp, "down": domain.StatusDown, "degraded": domain.StatusDegraded,
	}
	for raw, want := range cases {
		got, err := domain.ParsePushStatus(raw)
		if err != nil {
			t.Errorf("ParsePushStatus(%q) returned unexpected error: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParsePushStatus(%q) = %v, want %v", raw, got, want)
		}
	}
}

// "unknown" é o que o UpWatch diz quando não sabe; deixar quem reporta
// dizê-lo faria um serviço com problema sumir das contas de
// disponibilidade, que ignoram amostras desconhecidas.
func TestParsePushStatusRejectsUnknownAndGarbage(t *testing.T) {
	for _, raw := range []string{"unknown", "DOWN", "ok", "0"} {
		_, err := domain.ParsePushStatus(raw)

		var verr *domain.ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("ParsePushStatus(%q) error = %v, want a ValidationError", raw, err)
			continue
		}
		if verr.Field != "status" {
			t.Errorf("ParsePushStatus(%q) blamed field %q, want %q", raw, verr.Field, "status")
		}
	}
}

// A mensagem vem de fora e vai parar em aviso de chat e em e-mail. Sem
// limite, um script com defeito (ou alguém com o token) despejaria um
// log inteiro em cada notificação.
func TestCleanPushMessageLimitsLength(t *testing.T) {
	long := strings.Repeat("ç", domain.MaxPushMessageLength+50)

	got := domain.CleanPushMessage(long)

	if n := utf8.RuneCountInString(got); n > domain.MaxPushMessageLength {
		t.Errorf("message has %d runes, want at most %d", n, domain.MaxPushMessageLength)
	}
	// Cortar no meio de um caractere de vários bytes produziria texto
	// inválido, que alguns canais recusam inteiro.
	if !utf8.ValidString(got) {
		t.Error("truncated message is not valid UTF-8")
	}
}

// Quebra de linha e caractere de controle na causa permitiriam forjar
// linhas extras no aviso — um "RESOLVIDO" falso logo abaixo da queda.
func TestCleanPushMessageFlattensControlCharacters(t *testing.T) {
	got := domain.CleanPushMessage("fila parada\nRESOLVIDO\x00\x1b[31m")

	if strings.ContainsAny(got, "\n\r\x00\x1b") {
		t.Errorf("CleanPushMessage kept control characters: %q", got)
	}
	if !strings.Contains(got, "fila parada") {
		t.Errorf("CleanPushMessage = %q, want the original text preserved", got)
	}
}

func TestCleanPushMessageRepairsInvalidUTF8(t *testing.T) {
	got := domain.CleanPushMessage("fila \xff sem worker")

	if !utf8.ValidString(got) {
		t.Errorf("CleanPushMessage = %q, want valid UTF-8", got)
	}
}

func TestCleanPushMessageTrimsSpaces(t *testing.T) {
	if got := domain.CleanPushMessage("   fila sem worker  "); got != "fila sem worker" {
		t.Errorf("CleanPushMessage = %q, want %q", got, "fila sem worker")
	}
}

// Latência negativa não descreve duração nenhuma e entraria nos
// percentis puxando a média para baixo.
func TestParsePushLatencyRejectsNegativeAndGarbage(t *testing.T) {
	for _, raw := range []string{"-1", "rápido", "1.5"} {
		_, err := domain.ParsePushLatency(raw)

		var verr *domain.ValidationError
		if !errors.As(err, &verr) || verr.Field != "latency_ms" {
			t.Errorf("ParsePushLatency(%q) error = %v, want a latency_ms ValidationError", raw, err)
		}
	}
}

func TestParsePushLatencyAcceptsMilliseconds(t *testing.T) {
	got, err := domain.ParsePushLatency("1234")
	if err != nil {
		t.Fatalf("ParsePushLatency returned unexpected error: %v", err)
	}
	if got != 1234 {
		t.Errorf("ParsePushLatency = %d, want 1234", got)
	}
}

// Sem o parâmetro a latência é zero, que já significa "não medido" no
// resto do sistema.
func TestParsePushLatencyEmptyIsZero(t *testing.T) {
	got, err := domain.ParsePushLatency("")
	if err != nil || got != 0 {
		t.Errorf("ParsePushLatency(\"\") = (%d, %v), want (0, nil)", got, err)
	}
}
