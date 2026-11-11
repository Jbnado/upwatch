package domain

import (
	"slices"
	"time"
)

// TokenExpiryMilestones são as antecedências de aviso de vencimento de um
// token de API, em dias, do mais distante ao mais próximo.
//
// Decisão fechada: 30 e 7 dias. Trinta dá tempo de planejar a troca sem
// correr; sete é o lembrete de quem deixou passar — perto o bastante para
// virar tarefa, longe o bastante para não quebrar um script sem aviso.
var TokenExpiryMilestones = []int{30, 7}

// DueTokenExpiryMilestone decide o marco de aviso a enviar agora para um
// token que vence em expiresAt, dado o marco mais fino já avisado (warned,
// nil = nenhum). Devolve o marco em dias e true quando há aviso a enviar.
//
// O aviso sai uma vez por marco. O marco escolhido é o mais urgente cujo
// limiar já foi cruzado e que ainda não foi avisado: um token que surge já
// dentro de sete dias recebe um único aviso (o de sete), não os dois de
// uma vez. Token já vencido não gera aviso — aí o caminho é renovar ou
// revogar, não avisar.
func DueTokenExpiryMilestone(now, expiresAt time.Time, warned *int) (dias int, houve bool) {
	if !now.Before(expiresAt) {
		return 0, false
	}

	// Do mais próximo ao mais distante: o primeiro marco cujo limiar já
	// passou e que ainda não foi avisado vence a disputa. warned guarda o
	// marco mais fino já enviado, então um marco só vale se ninguém avisou
	// nele nem mais perto do vencimento.
	marcos := append([]int(nil), TokenExpiryMilestones...)
	slices.Sort(marcos)
	for _, m := range marcos {
		limiar := expiresAt.Add(-time.Duration(m) * 24 * time.Hour)
		if now.Before(limiar) {
			continue
		}
		if warned == nil || *warned > m {
			return m, true
		}
	}
	return 0, false
}
