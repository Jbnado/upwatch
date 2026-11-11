package auth_test

import (
	"context"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// O escopo do token não é congelado na emissão: AuthenticateToken resolve o
// acesso atual da conta a cada chamada. Rebaixar a conta tira o poder do
// token na hora, sem revogá-lo — ele passa a poder só o que a conta pode
// agora.
func TestAuthenticateTokenReflectsCurrentAccess(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t) // um admin de escopo total que sobra depois do rebaixamento
	ctx := context.Background()

	dono, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleAdmin, domain.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := f.svc.IssueToken(ctx, dono.ID, "ci", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Agora: admin de escopo total.
	if u, err := f.svc.AuthenticateToken(ctx, secret); err != nil || u.Role != domain.RoleAdmin || u.Scope.Restricted {
		t.Fatalf("antes do rebaixamento: %+v, %v", u, err)
	}

	// Rebaixa a conta a observador e restringe o escopo a um grupo.
	g := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := f.store.Groups().Create(ctx, &g); err != nil {
		t.Fatal(err)
	}
	escopo := domain.Scope{Restricted: true, GroupIDs: []int64{g.ID}}
	if _, err := f.svc.SetAccess(ctx, dono.ID, domain.RoleViewer, &escopo); err != nil {
		t.Fatal(err)
	}

	// O MESMO token reflete o acesso atual: observador, restrito. Se o
	// escopo tivesse sido congelado na emissão, voltaria admin de escopo
	// total.
	u, err := f.svc.AuthenticateToken(ctx, secret)
	if err != nil {
		t.Fatalf("o token deixou de autenticar após o rebaixamento: %v", err)
	}
	if u.Role != domain.RoleViewer || !u.Scope.Restricted || len(u.Scope.GroupIDs) != 1 {
		t.Fatalf("o acesso foi congelado na emissão: %+v", u)
	}
}
