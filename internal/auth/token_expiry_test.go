package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
)

// A instalação pode exigir validade e limitar o teto de vida de um token;
// IssueToken aplica essa política antes de gravar. Esta é a prova de que o
// serviço delega para domain.TokenExpiryPolicy, cujas regras têm teste
// próprio (ver internal/domain/token_expiry_test.go).
func TestIssueTokenEnforcesExpiryPolicy(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)

	svc := auth.New(f.store, auth.Options{
		Clock:       f.clock,
		TokenExpiry: domain.TokenExpiryPolicy{Require: true, MaxLifetime: 90 * 24 * time.Hour},
	})

	// Sem expiração, com a exigência ligada: recusado, e nada fica gravado —
	// a recusa acontece antes do Create.
	if _, _, err := svc.IssueToken(context.Background(), u.ID, "ci", nil); err == nil {
		t.Fatal("IssueToken aceitou token sem validade apesar de a instalação exigir")
	}
	if n := tokensDe(t, f, u.ID); n != 0 {
		t.Fatalf("a recusa deixou %d tokens gravados", n)
	}

	// Além do teto: recusado.
	longe := f.clock.Now().Add(100 * 24 * time.Hour)
	if _, _, err := svc.IssueToken(context.Background(), u.ID, "ci", &longe); err == nil {
		t.Fatal("IssueToken aceitou validade além do teto da instalação")
	}

	// Dentro do teto: aceito.
	dentro := f.clock.Now().Add(30 * 24 * time.Hour)
	if _, _, err := svc.IssueToken(context.Background(), u.ID, "ci", &dentro); err != nil {
		t.Fatalf("IssueToken recusou validade dentro do teto: %v", err)
	}
}

// Sem política (o padrão da instalação), um token sem validade continua
// sendo aceito: a validade só é exigida quando a instalação a liga.
func TestIssueTokenWithoutPolicyStillAllowsNoExpiry(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)

	if _, _, err := f.svc.IssueToken(context.Background(), u.ID, "ci", nil); err != nil {
		t.Fatalf("IssueToken recusou token sem validade sem política ligada: %v", err)
	}
}
