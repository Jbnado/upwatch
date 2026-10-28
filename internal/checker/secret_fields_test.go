package checker_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
)

// secretSamples são configurações com cada forma de credencial que um
// monitor guarda, e o valor de cada uma.
var secretSamples = []struct {
	typ     domain.MonitorType
	raw     string
	secrets []string
	public  []string
}{
	{domain.MonitorRedis, `{"password":"senha-redis","db":2}`, []string{"senha-redis"}, nil},
	{domain.MonitorPostgres, `{"username":"mon","Password":"senha-pg"}`, []string{"senha-pg"}, []string{"mon"}},
	{domain.MonitorMySQL, `{"username":"mon","password":"senha-mysql"}`, []string{"senha-mysql"}, []string{"mon"}},
	{
		domain.MonitorHTTP,
		`{"basic_auth_user":"ana","basic_auth_pass":"senha-http",` +
			`"headers":{"Authorization":"Bearer tok-auth","X-Api-Key":"tok-chave","Accept":"text/plain"}}`,
		[]string{"senha-http", "tok-auth", "tok-chave"},
		[]string{"ana", "text/plain"},
	},
	{domain.MonitorPush, `{"token":"token-do-push-123","period":"1m"}`, []string{"token-do-push-123"}, nil},
}

func sealSample(t *testing.T, typ domain.MonitorType, raw string) json.RawMessage {
	t.Helper()
	k, _ := secretbox.GenerateKey()
	box, _ := secretbox.New(k)
	sealed, err := box.SealConfig(json.RawMessage(raw), checker.SecretFields(typ), "monitor", 1)
	if err != nil {
		t.Fatalf("SealConfig(%s) returned unexpected error: %v", typ, err)
	}
	return sealed
}

// Cifrar em repouso precisa alcançar toda credencial que o monitor guarda:
// a senha do banco monitorado, a do basic auth, os cabeçalhos de
// credencial e o token do push. O que não é segredo segue legível, para
// o banco continuar inspecionável.
func TestSecretFieldsCoverEveryCredential(t *testing.T) {
	for _, s := range secretSamples {
		t.Run(s.typ.String(), func(t *testing.T) {
			sealed := string(sealSample(t, s.typ, s.raw))
			for _, secret := range s.secrets {
				if strings.Contains(sealed, secret) {
					t.Errorf("sealed config %s still carries %q", sealed, secret)
				}
			}
			for _, public := range s.public {
				if !strings.Contains(sealed, public) {
					t.Errorf("sealed config %s lost the public value %q", sealed, public)
				}
			}
		})
	}
}

// O que a API esconde de quem só lê e o que fica cifrado no banco saem da
// mesma lista. Com duas, um campo novo de senha entraria numa e não na
// outra: oculto na tela e em texto puro no backup, ou o contrário.
func TestSecretFieldsMatchWhatRedactConfigHides(t *testing.T) {
	for _, s := range secretSamples {
		t.Run(s.typ.String(), func(t *testing.T) {
			redacted := string(checker.RedactConfig(s.typ, json.RawMessage(s.raw), checker.Readers))
			sealed := string(sealSample(t, s.typ, s.raw))
			for _, value := range append(append([]string{}, s.secrets...), s.public...) {
				hidden := !strings.Contains(redacted, value)
				ciphered := !strings.Contains(sealed, value)
				if hidden != ciphered {
					t.Errorf("%q: hidden from readers = %v, ciphered at rest = %v", value, hidden, ciphered)
				}
			}
		})
	}
}

func TestSecretFieldsOfTypesWithoutCredential(t *testing.T) {
	for _, typ := range []domain.MonitorType{domain.MonitorTCP, domain.MonitorTLS, domain.MonitorDNS, domain.MonitorICMP} {
		if f := checker.SecretFields(typ); !f.Empty() {
			t.Errorf("SecretFields(%s) = %+v, want none", typ, f)
		}
	}
}
