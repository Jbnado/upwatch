package domain_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

func dur(d time.Duration) *time.Duration { return &d }

// O lembrete é configurado onde faz sentido configurá-lo: o sistema
// inteiro de produção de uma vez, com exceção para o monitor que só
// incomoda. Cada degrau precisa vencer o de cima, e o zero precisa parar a
// subida — senão "desligado" no monitor voltaria a herdar o do sistema.
func TestReminderIntervalMostSpecificWins(t *testing.T) {
	sistema := domain.Group{ID: 1, Name: "RPA", ReminderInterval: dur(time.Hour)}
	prod := domain.Group{ID: 2, Name: "prod", ParentID: idp(1)}
	hml := domain.Group{ID: 3, Name: "hml", ParentID: idp(1), ReminderInterval: dur(0)}
	arvore := domain.NewGroupTree([]domain.Group{sistema, prod, hml})

	casos := []struct {
		nome    string
		monitor domain.Monitor
		padrao  time.Duration
		quer    time.Duration
	}{
		{"sem grupo usa o padrão", domain.Monitor{}, 30 * time.Minute, 30 * time.Minute},
		{"ambiente sem escolha herda do sistema", domain.Monitor{GroupID: idp(2)}, 0, time.Hour},
		{"zero no ambiente desliga", domain.Monitor{GroupID: idp(3)}, 30 * time.Minute, 0},
		{"monitor vence o grupo", domain.Monitor{GroupID: idp(2), ReminderInterval: dur(10 * time.Minute)}, 0, 10 * time.Minute},
		{"zero no monitor desliga", domain.Monitor{GroupID: idp(2), ReminderInterval: dur(0)}, time.Hour, 0},
		{"grupo apagado cai no padrão", domain.Monitor{GroupID: idp(99)}, 15 * time.Minute, 15 * time.Minute},
	}
	for _, c := range casos {
		if got := domain.ReminderInterval(c.monitor, arvore, c.padrao); got != c.quer {
			t.Errorf("%s: intervalo = %s, quer %s", c.nome, got, c.quer)
		}
	}
}

func TestValidateReminderInterval(t *testing.T) {
	for _, ok := range []*time.Duration{nil, dur(0), dur(domain.MinReminderInterval), dur(domain.MaxReminderInterval)} {
		if err := domain.ValidateReminderInterval(ok); err != nil {
			t.Errorf("%v recusado: %v", ok, err)
		}
	}
	for _, ruim := range []time.Duration{time.Minute, -time.Minute, domain.MaxReminderInterval + time.Minute} {
		if err := domain.ValidateReminderInterval(&ruim); err == nil {
			t.Errorf("%s aceito", ruim)
		}
	}
}
