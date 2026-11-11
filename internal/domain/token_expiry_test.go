package domain_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// A política de validade da instalação decide, na criação de um token, se a
// expiração é exigida e qual é o teto de vida. Teto zero = sem teto.
func TestTokenExpiryPolicyCheckExpiry(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}

	for _, tt := range []struct {
		name      string
		policy    domain.TokenExpiryPolicy
		expiresAt *time.Time
		wantErr   bool
	}{
		{"sem exigência, sem expiração: passa", domain.TokenExpiryPolicy{}, nil, false},
		{"exigência ligada, sem expiração: falha", domain.TokenExpiryPolicy{Require: true}, nil, true},
		{"exigência ligada, com expiração: passa", domain.TokenExpiryPolicy{Require: true}, at(7 * 24 * time.Hour), false},
		{"dentro do teto: passa", domain.TokenExpiryPolicy{MaxLifetime: 90 * 24 * time.Hour}, at(30 * 24 * time.Hour), false},
		{"no limite do teto: passa", domain.TokenExpiryPolicy{MaxLifetime: 90 * 24 * time.Hour}, at(90 * 24 * time.Hour), false},
		{"além do teto: falha", domain.TokenExpiryPolicy{MaxLifetime: 90 * 24 * time.Hour}, at(100 * 24 * time.Hour), true},
		{"sem teto permite prazo longo: passa", domain.TokenExpiryPolicy{}, at(1000 * 24 * time.Hour), false},
		{"expiração no passado: falha", domain.TokenExpiryPolicy{}, at(-time.Hour), true},
		{"expiração igual a agora: falha", domain.TokenExpiryPolicy{}, &now, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.CheckExpiry(tt.expiresAt, now)
			if tt.wantErr && err == nil {
				t.Fatalf("esperava erro, veio nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("não esperava erro, veio %v", err)
			}
		})
	}
}

// Editar a expiração de um token existente: encurtar ou adicionar prazo é
// livre; esticar (afastar o vencimento, inclusive removê-lo) respeita o teto
// contado desde a criação e é sinalizado para a auditoria. O teto zero não
// limita, mas a exigência de validade ainda impede remover o prazo.
func TestTokenExpiryPolicyCheckExpiryEdit(t *testing.T) {
	created := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	now := created.Add(10 * 24 * time.Hour)
	at := func(d time.Duration) *time.Time {
		v := created.Add(d)
		return &v
	}
	const dia = 24 * time.Hour

	for _, tt := range []struct {
		name         string
		policy       domain.TokenExpiryPolicy
		current      *time.Time
		desired      *time.Time
		wantExtended bool
		wantErr      bool
	}{
		{"adicionar prazo a token sem validade: livre", domain.TokenExpiryPolicy{}, nil, at(30 * dia), false, false},
		{"encurtar prazo: livre", domain.TokenExpiryPolicy{}, at(30 * dia), at(20 * dia), false, false},
		{"esticar sem teto: permitido e sinalizado", domain.TokenExpiryPolicy{}, at(30 * dia), at(60 * dia), true, false},
		{"esticar dentro do teto: permitido e sinalizado", domain.TokenExpiryPolicy{MaxLifetime: 90 * dia}, at(30 * dia), at(80 * dia), true, false},
		{"esticar além do teto: recusado", domain.TokenExpiryPolicy{MaxLifetime: 90 * dia}, at(30 * dia), at(120 * dia), true, true},
		{"remover prazo sem teto nem exigência: esticar permitido", domain.TokenExpiryPolicy{}, at(30 * dia), nil, true, false},
		{"remover prazo com teto: recusado", domain.TokenExpiryPolicy{MaxLifetime: 90 * dia}, at(30 * dia), nil, true, true},
		{"remover prazo com exigência: recusado", domain.TokenExpiryPolicy{Require: true}, at(30 * dia), nil, true, true},
		{"adicionar além do teto é livre (nunca houve prazo)", domain.TokenExpiryPolicy{MaxLifetime: 90 * dia}, nil, at(120 * dia), false, false},
		{"expiração no passado: recusado", domain.TokenExpiryPolicy{}, at(30 * dia), at(5 * dia), false, true},
		{"sem prazo e segue sem prazo com exigência: no-op permitido", domain.TokenExpiryPolicy{Require: true}, nil, nil, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			extended, err := tt.policy.CheckExpiryEdit(created, tt.current, tt.desired, now)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("esperava erro, veio nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("não esperava erro, veio %v", err)
			}
			if extended != tt.wantExtended {
				t.Errorf("extended = %v, esperado %v", extended, tt.wantExtended)
			}
		})
	}
}
