package domain_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
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

// Caracteres de formatação invisíveis também forjam o aviso, só que sem
// aparecer: o separador de linha U+2028 quebra a linha no Slack como um
// "\n", a inversão bidi (U+202E) faz "falhou" ser lido de trás para a
// frente, e o espaço de largura zero (U+200B) esconde que "@everyone" não
// é o texto que parece. unicode.IsControl não pega nenhum deles — são das
// categorias Cf, Zl e Zp, e não Cc.
func TestCleanPushMessageDropsInvisibleFormatting(t *testing.T) {
	raw := "fila\u2028RESOLVIDO\u2029ok \u202Eodnalaf\u202C ze\u200Bro \u2066iso\u2069 \ufeffbom"

	got := domain.CleanPushMessage(raw)

	for _, r := range got {
		if unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			t.Errorf("CleanPushMessage kept %U in %q", r, got)
		}
	}
	if !strings.Contains(got, "fila") || !strings.Contains(got, "RESOLVIDO") {
		t.Errorf("CleanPushMessage = %q, want the visible text preserved", got)
	}
}

// A latência vira time.Duration nas contas de degradação, e um valor
// absurdo multiplicado por milhão estoura o int64 e volta negativo: o
// monitor passaria a parecer rapidíssimo justamente quando o script mediu
// algo que não faz sentido. Nenhuma ida e volta legítima passa de um dia.
func TestParsePushLatencyRejectsAboveOneDay(t *testing.T) {
	for _, raw := range []string{"86400001", "9223372036854775807"} {
		_, err := domain.ParsePushLatency(raw)

		var verr *domain.ValidationError
		if !errors.As(err, &verr) || verr.Field != "latency_ms" {
			t.Errorf("ParsePushLatency(%q) error = %v, want a latency_ms ValidationError", raw, err)
		}
	}

	if got, err := domain.ParsePushLatency("86400000"); err != nil || got != 86400000 {
		t.Errorf("ParsePushLatency(one day) = (%d, %v), want it accepted", got, err)
	}
}
