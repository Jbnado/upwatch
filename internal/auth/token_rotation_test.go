package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Rotacionar emite um substituto e mantém o antigo válido na sobreposição,
// enquanto o sucessor ainda não foi usado: a rotação não deixa uma janela em
// que a automação para entre emitir o novo e trocar o segredo.
func TestRotateTokenKeepsBothValidDuringOverlap(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	old, oldSecret, err := f.svc.IssueToken(ctx, u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}

	novo, novoSecret, err := f.svc.RotateToken(ctx, u.ID, old.ID, nil)
	if err != nil {
		t.Fatalf("RotateToken returned unexpected error: %v", err)
	}
	if novoSecret == oldSecret || novo.ID == old.ID {
		t.Fatal("a rotação não gerou credencial nova")
	}
	if novo.Name != "ci" {
		t.Errorf("o substituto não herdou o nome: %q", novo.Name)
	}

	// Com o sucessor ainda não usado, os dois autenticam.
	if _, err := f.svc.AuthenticateToken(ctx, oldSecret); err != nil {
		t.Errorf("o token antigo deixou de valer na sobreposição: %v", err)
	}
	if _, err := f.svc.AuthenticateToken(ctx, novoSecret); err != nil {
		t.Errorf("o token novo não vale: %v", err)
	}
}

// Rotacionar duas vezes o mesmo token é recusado: rotaciona-se o substituto,
// não o antigo de novo.
func TestRotateTokenRefusesAlreadyRotated(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	old, _, err := f.svc.IssueToken(ctx, u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RotateToken(ctx, u.ID, old.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RotateToken(ctx, u.ID, old.ID, nil); !errors.Is(err, auth.ErrTokenAlreadyRotated) {
		t.Errorf("rotacionar de novo = %v, want ErrTokenAlreadyRotated", err)
	}

	// Rotacionar token de outra conta não acha nada.
	outra := outraConta(t, f, "bia")
	if _, _, err := f.svc.RotateToken(ctx, outra.ID, old.ID, nil); err == nil {
		t.Error("rotacionar token alheio foi aceito")
	}
}

// O substituto obedece à política de validade, como uma emissão nova.
func TestRotateTokenReplacementRespectsPolicy(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	svc := auth.New(f.store, auth.Options{
		Clock:       f.clock,
		TokenExpiry: domain.TokenExpiryPolicy{MaxLifetime: 90 * 24 * time.Hour},
	})
	old, _, err := svc.IssueToken(ctx, u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}

	alem := f.clock.Now().Add(120 * 24 * time.Hour)
	if _, _, err := svc.RotateToken(ctx, u.ID, old.ID, &alem); err == nil {
		t.Fatal("rotacionar com validade além do teto foi aceito")
	}
}

// Depois de o cliente migrar — usar o token novo —, usar o antigo é reuso:
// nega com TokenReuseError e revoga o antigo, que é a credencial vazada.
func TestReusedRotatedTokenIsRevokedAndDenied(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	old, oldSecret, err := f.svc.IssueToken(ctx, u.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, novoSecret, err := f.svc.RotateToken(ctx, u.ID, old.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// O cliente migra: usa o segredo novo.
	if _, err := f.svc.AuthenticateToken(ctx, novoSecret); err != nil {
		t.Fatal(err)
	}

	// Agora usar o antigo é sinal de vazamento.
	_, err = f.svc.AuthenticateToken(ctx, oldSecret)
	var reuse *auth.TokenReuseError
	if !errors.As(err, &reuse) {
		t.Fatalf("uso do antigo após a migração = %v, want TokenReuseError", err)
	}
	if reuse.TokenName != "ci" {
		t.Errorf("nome no erro = %q, want ci", reuse.TokenName)
	}

	// O antigo foi revogado; o novo segue valendo.
	if _, err := f.svc.AuthenticateToken(ctx, oldSecret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("o antigo ainda autentica após o reuso: %v", err)
	}
	if _, err := f.svc.AuthenticateToken(ctx, novoSecret); err != nil {
		t.Errorf("o novo deixou de valer: %v", err)
	}
}
