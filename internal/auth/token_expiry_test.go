package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// tokenByID devolve o token da conta pelo id, para conferir o que ficou
// gravado.
func tokenByID(t *testing.T, f *fixture, userID, id int64) domain.APIToken {
	t.Helper()

	list, err := f.store.Tokens().List(context.Background(), userID)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	for _, tk := range list {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("token %d não encontrado", id)
	return domain.APIToken{}
}

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

// O dono edita a validade do próprio token: adicionar prazo é livre e não
// conta como esticão, e editar token de outra conta não acha nada.
func TestUpdateTokenExpiry(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	tok, _, err := f.svc.IssueToken(ctx, u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}

	prazo := f.clock.Now().Add(30 * 24 * time.Hour)
	extended, err := f.svc.UpdateTokenExpiry(ctx, u.ID, tok.ID, &prazo)
	if err != nil {
		t.Fatalf("UpdateTokenExpiry recusou adicionar prazo: %v", err)
	}
	if extended {
		t.Error("adicionar prazo foi tratado como esticão")
	}
	if got := tokenByID(t, f, u.ID, tok.ID); got.ExpiresAt == nil || !got.ExpiresAt.Equal(prazo) {
		t.Fatalf("prazo não gravado: %v", got.ExpiresAt)
	}

	// Token de outra conta não é alcançado: editar por id não pode virar
	// porta para mexer na credencial alheia.
	outra := outraConta(t, f, "bia")
	if _, err := f.svc.UpdateTokenExpiry(ctx, outra.ID, tok.ID, &prazo); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("editar token alheio = %v, want ErrNotFound", err)
	}
}

// Esticar respeita o teto contado desde a criação: além do teto recusa sem
// gravar, dentro do teto grava e sinaliza o esticão para a auditoria.
func TestUpdateTokenExpiryRespectsCeilingOnStretch(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	svc := auth.New(f.store, auth.Options{
		Clock:       f.clock,
		TokenExpiry: domain.TokenExpiryPolicy{MaxLifetime: 90 * 24 * time.Hour},
	})

	curto := f.clock.Now().Add(30 * 24 * time.Hour)
	tok, _, err := svc.IssueToken(ctx, u.ID, "ci", &curto)
	if err != nil {
		t.Fatal(err)
	}

	alem := tok.CreatedAt.Add(120 * 24 * time.Hour)
	if _, err := svc.UpdateTokenExpiry(ctx, u.ID, tok.ID, &alem); err == nil {
		t.Fatal("esticar além do teto foi aceito")
	}
	if got := tokenByID(t, f, u.ID, tok.ID); got.ExpiresAt == nil || !got.ExpiresAt.Equal(curto) {
		t.Errorf("a recusa mexeu na validade: %v", got.ExpiresAt)
	}

	dentro := tok.CreatedAt.Add(80 * 24 * time.Hour)
	extended, err := svc.UpdateTokenExpiry(ctx, u.ID, tok.ID, &dentro)
	if err != nil {
		t.Fatalf("esticar dentro do teto recusado: %v", err)
	}
	if !extended {
		t.Error("esticar dentro do teto não foi sinalizado")
	}
}
