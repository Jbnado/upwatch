package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/secretbox"
)

// channelSamples são configurações de cada canal, com o que é credencial
// e o que não é.
var channelSamples = map[string]struct {
	raw     string
	secrets []string
	public  []string
}{
	"webhook": {
		`{"url":"https://hooks.exemplo.com/t/tok-webhook","headers":{"X-Qualquer":"tok-cabecalho"},"template":"{{.Monitor.Name}}"}`,
		[]string{"tok-webhook", "tok-cabecalho"}, []string{"{{.Monitor.Name}}"},
	},
	"discord": {
		`{"url":"https://discord.com/api/webhooks/1/tok-discord"}`,
		[]string{"tok-discord"}, nil,
	},
	"slack": {
		`{"url":"https://hooks.slack.com/services/T0/B0/tok-slack","template":"alerta"}`,
		[]string{"tok-slack"}, []string{"alerta"},
	},
	"email": {
		`{"host":"smtp.exemplo.com","username":"alertas","password":"senha-smtp","from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		[]string{"senha-smtp"}, []string{"smtp.exemplo.com", "alertas", "b@exemplo.com"},
	},
	"ntfy": {
		`{"server":"https://ntfy.exemplo.com","topic":"plantao","token":"tk_ntfy"}`,
		[]string{"tk_ntfy"}, []string{"plantao", "ntfy.exemplo.com"},
	},
}

// O canal guarda a credencial de quem pode publicar no destino: a senha
// SMTP, o token do ntfy e as URLs de webhook do Discord, do Slack e do
// genérico, que levam o token no próprio caminho. Os cabeçalhos do webhook
// existem para autenticar contra o destino, então todos contam.
func TestChannelSecretFieldsCoverEveryCredential(t *testing.T) {
	k, _ := secretbox.GenerateKey()
	box, _ := secretbox.New(k)

	for _, typ := range notifier.Types() {
		t.Run(typ, func(t *testing.T) {
			s, ok := channelSamples[typ]
			if !ok {
				t.Fatalf("canal %q sem amostra: diga aqui o que nele é credencial", typ)
			}
			sealed, err := box.SealConfig(json.RawMessage(s.raw), notifier.SecretFields(typ), "notification_channel", 1)
			if err != nil {
				t.Fatalf("SealConfig returned unexpected error: %v", err)
			}
			for _, secret := range s.secrets {
				if strings.Contains(string(sealed), secret) {
					t.Errorf("sealed config %s still carries %q", sealed, secret)
				}
			}
			for _, public := range s.public {
				if !strings.Contains(string(sealed), public) {
					t.Errorf("sealed config %s lost the public value %q", sealed, public)
				}
			}
		})
	}
}

// O tipo é normalizado como Build o normaliza: um canal gravado como
// "Discord" é montado como discord, e a URL dele é credencial do mesmo
// jeito.
func TestChannelSecretFieldsNormalizeTheType(t *testing.T) {
	if notifier.SecretFields(" Discord ").Empty() {
		t.Error("SecretFields(\" Discord \") found nothing to cipher")
	}
	if !notifier.SecretFields("pombo-correio").Empty() {
		t.Error("SecretFields of an unknown type is not empty")
	}
}
