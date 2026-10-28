package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
)

func (f *fixture) grupo(t *testing.T, nome string) int64 {
	t.Helper()
	g := domain.Group{Name: nome, Slug: domain.Slugify(nome)}
	if err := f.store.Groups().Create(context.Background(), &g); err != nil {
		t.Fatal(err)
	}
	return g.ID
}

// Administrador com escopo não cria grupo de topo, não mexe em canal nem
// em página pública. Se o último administrador que alcança tudo pudesse
// ser restringido, a instalação ficaria sem ninguém capaz disso — e a
// única saída seria mexer no banco à mão.
func TestSetAccessRefusesToRestrictTheLastFullAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.withAdmin(t)
	g := f.grupo(t, "RPA")

	restrito := domain.Scope{Restricted: true, GroupIDs: []int64{g}}
	if _, err := f.svc.SetAccess(ctx, admin.ID, domain.RoleAdmin, &restrito); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("restricting the last full admin returned %v, want ErrLastAdmin", err)
	}

	// Um administrador restrito ao lado não conta: ele também não
	// alcança o que só o escopo total alcança.
	if _, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleAdmin, restrito); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetAccess(ctx, admin.ID, domain.RoleViewer, nil); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("demoting the last full admin returned %v, want ErrLastAdmin", err)
	}
	if err := f.svc.DeleteUser(ctx, admin.ID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("deleting the last full admin returned %v, want ErrLastAdmin", err)
	}

	if _, err := f.svc.CreateUser(ctx, "caio", goodPassword, domain.RoleAdmin, domain.Scope{}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.SetAccess(ctx, admin.ID, domain.RoleAdmin, &restrito)
	if err != nil {
		t.Fatalf("restricting with another full admin around returned %v", err)
	}
	if !got.Scope.Restricted {
		t.Error("the returned account kept the full scope")
	}
}

// Sem escopo no pedido, o papel muda e o escopo fica: trocar papel não
// pode alargar o que a conta alcança por esquecimento de um campo.
func TestSetAccessKeepsScopeWhenOmitted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.withAdmin(t)
	g := f.grupo(t, "RPA")

	u, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleViewer,
		domain.Scope{Restricted: true, GroupIDs: []int64{g}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.SetAccess(ctx, u.ID, domain.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != domain.RoleAdmin || !got.Scope.Restricted || len(got.Scope.GroupIDs) != 1 {
		t.Errorf("account after role change = %+v", got)
	}
}
