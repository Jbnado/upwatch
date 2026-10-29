package domain

import (
	"fmt"
	"time"
)

// Limites do intervalo de lembrete.
//
// O piso existe porque lembrete a cada minuto é a mesma queda virando
// spam, e quem recebe passa a silenciar o canal — inclusive para a
// próxima queda. O teto é uma semana: mais que isso já não é lembrete.
const (
	MinReminderInterval = 5 * time.Minute
	MaxReminderInterval = 7 * 24 * time.Hour
)

// ValidateReminderInterval confere o intervalo escolhido. Nulo herda e
// zero desliga; os dois são escolhas válidas.
func ValidateReminderInterval(d *time.Duration) error {
	if d == nil || *d == 0 {
		return nil
	}
	if *d < MinReminderInterval || *d > MaxReminderInterval {
		return invalid("reminder_minutes", fmt.Sprintf("deve ficar entre %d e %d minutos, ou 0 para desligar",
			int(MinReminderInterval.Minutes()), int(MaxReminderInterval.Minutes())))
	}
	return nil
}

// ReminderInterval é de quanto em quanto tempo uma queda aberta do monitor
// é relembrada. Zero não relembra.
//
// O mais específico vence: o monitor, depois o ambiente, depois o sistema,
// e por fim o padrão da instalação. Zero gravado em qualquer degrau é
// escolha, e para a subida ali — é como um monitor barulhento sai do
// lembrete que vale para o resto do sistema.
func ReminderInterval(m Monitor, tree GroupTree, fallback time.Duration) time.Duration {
	if m.ReminderInterval != nil {
		return *m.ReminderInterval
	}
	vistos := map[int64]bool{}
	for p := m.GroupID; p != nil && !vistos[*p]; {
		vistos[*p] = true
		g, ok := tree.Get(*p)
		if !ok {
			break
		}
		if g.ReminderInterval != nil {
			return *g.ReminderInterval
		}
		p = g.ParentID
	}
	return fallback
}
