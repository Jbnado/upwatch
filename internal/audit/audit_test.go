package audit_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/audit"
)

func captura() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// Quem coleta o log procura a mensagem e filtra pelos campos. Uma linha
// que sai sem um deles deixa de casar com o filtro, e o evento existe no
// arquivo sem existir para quem audita.
func TestRegistraEscreveAMensagemEOsQuatroCampos(t *testing.T) {
	log, buf := captura()

	audit.Registra(log, audit.Evento{
		Ator: "admin", Ação: "2fa.ligada", Alvo: "observadora", Origem: "203.0.113.7",
	})

	linha := buf.String()
	if !strings.Contains(linha, "evento de segurança") {
		t.Errorf("linha sem a mensagem que quem coleta procura: %s", linha)
	}
	for campo, valor := range map[string]string{
		"ação": "2fa.ligada", "ator": "admin",
		"alvo": "observadora", "origem": "203.0.113.7",
	} {
		if !strings.Contains(linha, campo+"="+valor) {
			t.Errorf("linha não traz %s=%s: %s", campo, valor, linha)
		}
	}
}

// Campo vazio num coletor passa por campo ausente, e a linha deixa de
// casar com o filtro de quem procura ação sem conta identificada.
func TestRegistraChamaOAtorVazioDeAnônimo(t *testing.T) {
	log, buf := captura()

	audit.Registra(log, audit.Evento{Ação: "setup.token_errado", Alvo: "/setup", Origem: "127.0.0.1"})

	if !strings.Contains(buf.String(), "ator="+audit.AtorAnônimo) {
		t.Errorf("linha não traz ator=%s: %s", audit.AtorAnônimo, buf.String())
	}
}

// Todas as ações num lugar só é o que impede o servidor e a linha de
// comando de escreverem nomes diferentes para a mesma coisa. Nomes
// repetidos quebrariam isso de outro jeito: dois eventos distintos
// cairiam no mesmo filtro.
func TestAsAçõesNãoSeRepetem(t *testing.T) {
	ações := []string{
		audit.AçãoLoginSenhaErrada, audit.AçãoLoginCódigoErrado,
		audit.AçãoLimiteBloqueio, audit.AçãoContaBloqueada,
		audit.AçãoTokenCriado, audit.AçãoTokenRevogado,
		audit.AçãoSenhaTrocada, audit.AçãoUsuárioCriado, audit.AçãoAcessoTrocado,
		audit.AçãoSetupTokenErrado, audit.AçãoMetricsTokenErrado,
		audit.Ação2FALigada, audit.Ação2FADesligada,
		audit.Ação2FARecuperaçãoNova, audit.Ação2FARecuperaçãoUsada,
		audit.Ação2FADesligadaPorAdmin, audit.Ação2FADesligadaPelaLinhaDeComando,
	}

	vistas := make(map[string]bool, len(ações))
	for _, a := range ações {
		if a == "" {
			t.Error("ação vazia na lista")
			continue
		}
		if vistas[a] {
			t.Errorf("ação %q aparece em mais de uma constante", a)
		}
		vistas[a] = true
	}
}
