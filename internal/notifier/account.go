package notifier

import (
	"fmt"

	"github.com/Jbnado/upwatch/internal/domain"
)

// accountTitle é o título curto do aviso de conta, para o assunto do
// e-mail, o título do ntfy e o cabeçalho do cartão do Teams — onde a frase
// inteira não cabe e um rótulo diz de relance do que se trata.
func accountTitle(a AccountNotice) string {
	if a.Event == domain.EventAccountTokenRevoked {
		return "Token de API revogado"
	}
	return "Token de API vencendo"
}

// renderAccount escreve o aviso de conta em uma frase, como Render faz para
// os monitores: o que é, de quem e quando — nessa ordem, sem preâmbulo.
func renderAccount(a AccountNotice) string {
	quem := ""
	if a.Username != "" {
		quem = " de " + a.Username
	}
	if a.Event == domain.EventAccountTokenRevoked {
		return fmt.Sprintf("O token de API %q%s foi revogado.", a.TokenName, quem)
	}
	return fmt.Sprintf("O token de API %q%s %s (%s).",
		a.TokenName, quem, vencimentoEm(a.DaysLeft), a.ExpiresAt.Format("02/01/2006"))
}

// vencimentoEm escreve quanto falta para o vencimento, como se fala: o
// aviso é lido no celular, e "vence em 7 dias" chega melhor que uma data
// solta.
func vencimentoEm(dias int) string {
	switch {
	case dias <= 0:
		return "vence hoje"
	case dias == 1:
		return "vence amanhã"
	default:
		return fmt.Sprintf("vence em %d dias", dias)
	}
}
