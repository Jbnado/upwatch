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
