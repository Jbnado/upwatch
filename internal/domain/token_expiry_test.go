package domain

import (
	"testing"
	"time"
)

// O marco de aviso devido é o mais urgente ainda não avisado: o aviso sai
// uma vez por marco (30 e 7 dias), e um token que surge já dentro de sete
// dias recebe um único aviso, não os dois de uma vez.
func TestDueTokenExpiryMilestone(t *testing.T) {
	const dia = 24 * time.Hour
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	marco := func(v int) *int { return &v }

	casos := []struct {
		nome      string
		venceEm   time.Duration
		avisado   *int
		wantDias  int
		wantHouve bool
	}{
		{"longe demais", 40 * dia, nil, 0, false},
		{"dentro de 30, nunca avisado", 20 * dia, nil, 30, true},
		{"dentro de 30, já avisado em 30", 20 * dia, marco(30), 0, false},
		{"dentro de 7, avisado em 30", 5 * dia, marco(30), 7, true},
		{"dentro de 7, nunca avisado, vai no mais urgente", 5 * dia, nil, 7, true},
		{"dentro de 7, já avisado em 7", 5 * dia, marco(7), 0, false},
		{"exatamente 30 dias", 30 * dia, nil, 30, true},
		{"exatamente 7 dias, avisado em 30", 7 * dia, marco(30), 7, true},
		{"já vencido", -1 * dia, nil, 0, false},
		{"vence agora", 0, nil, 0, false},
	}

	for _, c := range casos {
		gotDias, gotHouve := DueTokenExpiryMilestone(base, base.Add(c.venceEm), c.avisado)
		if gotDias != c.wantDias || gotHouve != c.wantHouve {
			t.Errorf("%s: DueTokenExpiryMilestone = (%d, %v), want (%d, %v)",
				c.nome, gotDias, gotHouve, c.wantDias, c.wantHouve)
		}
	}
}
