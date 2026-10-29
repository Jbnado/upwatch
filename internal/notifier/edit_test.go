package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// O formulário de edição recebe a configuração sem nenhuma credencial, e
// com tudo o que não é credencial: sem isso, editar um campo obrigaria a
// redigitar os outros.
func TestRedactConfigHidesEveryCredential(t *testing.T) {
	for _, typ := range notifier.Types() {
		t.Run(typ, func(t *testing.T) {
			s := channelSamples[typ]
			out := string(notifier.RedactConfig(typ, json.RawMessage(s.raw)))
			if out == "" {
				t.Fatal("RedactConfig returned nothing for a valid config")
			}
			for _, secret := range s.secrets {
				if strings.Contains(out, secret) {
					t.Errorf("redacted config %s still carries %q", out, secret)
				}
			}
			for _, public := range s.public {
				if !strings.Contains(out, public) {
					t.Errorf("redacted config %s lost the public value %q", out, public)
				}
			}
		})
	}
}

func TestRedactConfigMarksWhereTheSecretWas(t *testing.T) {
	casos := []struct{ typ, raw, quer string }{
		{"discord", `{"url":"https://discord.com/api/webhooks/1/tok"}`, `"url_set":true`},
		{"teams", `{"url":"https://prod-1.westus.logic.azure.com/workflows/w1?sig=tok"}`, `"url_set":true`},
		{"webhook", `{"url":"https://h.exemplo.com","headers":{"Authorization":"Bearer x","X-Vazio":""}}`,
			`"headers":{"Authorization":"***","X-Vazio":""}`},
		{"email", `{"host":"smtp.exemplo.com","password":"p","from":"a@exemplo.com","to":["b@exemplo.com"]}`, `"password_set":true`},
		{"email", `{"host":"smtp.exemplo.com","from":"a@exemplo.com","to":["b@exemplo.com"]}`, `"password_set":false`},
		{"ntfy", `{"topic":"plantao-infra-x7k2","token":"tk"}`, `"token_set":true`},
	}
	for _, c := range casos {
		out := string(notifier.RedactConfig(c.typ, json.RawMessage(c.raw)))
		if !strings.Contains(out, c.quer) {
			t.Errorf("RedactConfig(%s) = %s, want it to contain %s", c.typ, out, c.quer)
		}
	}
}

// Uma configuração que não se lê não sai crua: pode ser justamente a que
// carrega o segredo.
func TestRedactConfigDropsWhatItCannotRead(t *testing.T) {
	if out := notifier.RedactConfig("email", json.RawMessage(`"senha-solta"`)); out != nil {
		t.Errorf("RedactConfig of an unreadable config = %s, want nil", out)
	}
	if out := notifier.RedactConfig("pombo-correio", json.RawMessage(`{"url":"x"}`)); out != nil {
		t.Errorf("RedactConfig of an unknown type = %s, want nil", out)
	}
}

func carry(t *testing.T, typ, next, prev string) map[string]any {
	t.Helper()
	out, err := notifier.CarrySecrets(typ, json.RawMessage(next), typ, json.RawMessage(prev))
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("CarrySecrets returned invalid JSON %s: %v", out, err)
	}
	return cfg
}

func TestCarrySecretsKeepsWhatDidNotComeBack(t *testing.T) {
	t.Run("webhook url and masked header", func(t *testing.T) {
		cfg := carry(t, "webhook",
			`{"headers":{"Authorization":"***","X-Novo":"v"},"template":"novo"}`,
			`{"url":"https://h.exemplo.com/tok","headers":{"Authorization":"Bearer guardado"}}`)
		if cfg["url"] != "https://h.exemplo.com/tok" {
			t.Errorf("url = %v, want the stored one", cfg["url"])
		}
		h := cfg["headers"].(map[string]any)
		if h["Authorization"] != "Bearer guardado" || h["X-Novo"] != "v" {
			t.Errorf("headers = %v, want the stored Authorization and the new X-Novo", h)
		}
		if cfg["template"] != "novo" {
			t.Errorf("template = %v, want the edited one", cfg["template"])
		}
	})

	t.Run("email password", func(t *testing.T) {
		cfg := carry(t, "email",
			`{"host":"SMTP.exemplo.com","username":"u","from":"a@exemplo.com","to":["c@exemplo.com"]}`,
			`{"host":"smtp.exemplo.com","username":"u","password":"guardada","from":"a@exemplo.com","to":["b@exemplo.com"]}`)
		if cfg["password"] != "guardada" {
			t.Errorf("password = %v, want the stored one", cfg["password"])
		}
	})

	t.Run("ntfy token", func(t *testing.T) {
		cfg := carry(t, "ntfy",
			`{"server":"https://ntfy.exemplo.com/","topic":"outro"}`,
			`{"server":"https://ntfy.exemplo.com","topic":"plantao","token":"tk_guardado"}`)
		if cfg["token"] != "tk_guardado" || cfg["topic"] != "outro" {
			t.Errorf("config = %v, want the stored token and the new topic", cfg)
		}
	})
}

// Campo enviado é o valor novo, mesmo vazio: é assim que se tira a senha.
func TestCarrySecretsTakesWhatWasSent(t *testing.T) {
	cfg := carry(t, "email",
		`{"host":"smtp.exemplo.com","Password":"nova","from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		`{"host":"smtp.exemplo.com","password":"guardada","from":"a@exemplo.com","to":["b@exemplo.com"]}`)
	if cfg["password"] != "nova" {
		t.Errorf("password = %v, want the one sent", cfg["password"])
	}

	cfg = carry(t, "discord",
		`{"url":"https://discord.com/api/webhooks/2/novo"}`,
		`{"url":"https://discord.com/api/webhooks/1/guardado"}`)
	if cfg["url"] != "https://discord.com/api/webhooks/2/novo" {
		t.Errorf("url = %v, want the one sent", cfg["url"])
	}
}

// A credencial guardada não vai para outro destino: quem edita canais
// levaria o token de um cabeçalho, ou a senha do SMTP, para um servidor
// seu.
func TestCarrySecretsRefusesToFollowANewDestination(t *testing.T) {
	casos := []struct{ typ, next, prev string }{
		{"webhook",
			`{"url":"https://atacante.exemplo","headers":{"Authorization":"***"}}`,
			`{"url":"https://h.exemplo.com","headers":{"Authorization":"Bearer guardado"}}`},
		{"email",
			`{"host":"smtp.atacante.exemplo","username":"u","from":"a@exemplo.com","to":["b@exemplo.com"]}`,
			`{"host":"smtp.exemplo.com","username":"u","password":"guardada","from":"a@exemplo.com","to":["b@exemplo.com"]}`},
		{"ntfy",
			`{"server":"https://ntfy.atacante.exemplo","topic":"plantao"}`,
			`{"server":"https://ntfy.exemplo.com","topic":"plantao","token":"tk_guardado"}`},
	}
	for _, c := range casos {
		t.Run(c.typ, func(t *testing.T) {
			out, err := notifier.CarrySecrets(c.typ, json.RawMessage(c.next), c.typ, json.RawMessage(c.prev))
			if err == nil {
				t.Fatalf("CarrySecrets carried the secret to a new destination: %s", out)
			}
		})
	}
}

func TestCarrySecretsRefusesAMaskWithNothingStored(t *testing.T) {
	_, err := notifier.CarrySecrets("webhook",
		json.RawMessage(`{"url":"https://h.exemplo.com","headers":{"X-Token":"***"}}`),
		"webhook", json.RawMessage(`{"url":"https://h.exemplo.com"}`))
	if err == nil {
		t.Error("CarrySecrets accepted a masked header with no stored value")
	}
}

// Na troca de tipo a configuração é outra, e a URL de um Discord não vira
// a de um webhook genérico por omissão.
func TestCarrySecretsKeepsNothingAcrossTypes(t *testing.T) {
	out, err := notifier.CarrySecrets("webhook", json.RawMessage(`{"template":"x"}`),
		"discord", json.RawMessage(`{"url":"https://discord.com/api/webhooks/1/tok"}`))
	if err != nil {
		t.Fatalf("CarrySecrets returned unexpected error: %v", err)
	}
	if strings.Contains(string(out), "tok") {
		t.Errorf("CarrySecrets carried the discord url into a webhook: %s", out)
	}
}
