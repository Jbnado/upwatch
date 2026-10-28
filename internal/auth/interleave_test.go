package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// Operações de conta intercaladas.
//
// Cada operação lê a conta, decide e grava. Se outra grava no meio, a
// primeira não pode desfazer o que a segunda fez: trocar a própria senha
// enquanto um administrador retira o acesso da conta regravava o papel e
// o escopo antigos, e a revogação sumia sem ninguém ver.

// intercalado roda outra operação logo antes da primeira escrita de conta.
type intercalado struct {
	store.Store
	once  sync.Once
	antes func()
}

func (s *intercalado) Users() store.UserRepo {
	return &usuariosIntercalados{UserRepo: s.Store.Users(), s: s}
}

type usuariosIntercalados struct {
	store.UserRepo
	s *intercalado
}

func (u *usuariosIntercalados) SetPasswordHash(ctx context.Context, id int64, hash string) error {
	u.s.once.Do(u.s.antes)
	return u.UserRepo.SetPasswordHash(ctx, id, hash)
}

func (u *usuariosIntercalados) SetAccess(ctx context.Context, id int64, role domain.Role, scope *domain.Scope) error {
	u.s.once.Do(u.s.antes)
	return u.UserRepo.SetAccess(ctx, id, role, scope)
}

func (u *usuariosIntercalados) Delete(ctx context.Context, id int64) error {
	u.s.once.Do(u.s.antes)
	return u.UserRepo.Delete(ctx, id)
}

// servicoIntercalado devolve um serviço cuja primeira escrita espera antes
// rodar no serviço direto.
func (f *fixture) servicoIntercalado(antes func()) *auth.Service {
	return auth.New(&intercalado{Store: f.store, antes: antes}, auth.Options{Clock: f.clock})
}

func TestPasswordChangeDoesNotUndoARevocation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.withAdmin(t)
	g := f.grupo(t, "RPA")

	u, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleAdmin, domain.Scope{})
	if err != nil {
		t.Fatal(err)
	}

	restrito := domain.Scope{Restricted: true, GroupIDs: []int64{g}}
	svc := f.servicoIntercalado(func() {
		if _, err := f.svc.SetAccess(ctx, u.ID, domain.RoleViewer, &restrito); err != nil {
			t.Errorf("a revogação no meio falhou: %v", err)
		}
	})
	if err := svc.ChangePassword(ctx, u.ID, goodPassword, "outra-senha-bem-longa"); err != nil {
		t.Fatalf("ChangePassword returned unexpected error: %v", err)
	}

	got, err := f.store.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != domain.RoleViewer || !got.Scope.Restricted {
		t.Errorf("depois da troca de senha a conta voltou a %v com escopo %+v; a revogação se perdeu", got.Role, got.Scope)
	}
	if !got.CheckPassword("outra-senha-bem-longa") {
		t.Error("a senha nova não ficou gravada")
	}
}

// O caminho inverso: retirar acesso enquanto a pessoa troca a senha não
// pode ressuscitar a senha antiga — que é justamente a que ela quis
// abandonar por suspeitar que vazou.
func TestRevocationDoesNotRestoreTheOldPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.withAdmin(t)

	u, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleAdmin, domain.Scope{})
	if err != nil {
		t.Fatal(err)
	}

	svc := f.servicoIntercalado(func() {
		if err := f.svc.ChangePassword(ctx, u.ID, goodPassword, "outra-senha-bem-longa"); err != nil {
			t.Errorf("a troca de senha no meio falhou: %v", err)
		}
	})
	if _, err := svc.SetAccess(ctx, u.ID, domain.RoleViewer, nil); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}

	got, err := f.store.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckPassword(goodPassword) || !got.CheckPassword("outra-senha-bem-longa") {
		t.Error("a revogação regravou a senha antiga")
	}
	if got.Role != domain.RoleViewer {
		t.Errorf("papel = %v, want viewer", got.Role)
	}
}

// Dois administradores de escopo total se rebaixam ao mesmo tempo. Cada
// um confere que sobra o outro, e os dois gravam: a instalação ficava sem
// ninguém capaz de criar grupo de topo ou mexer em canal. A conferência e
// a escrita precisam ser uma coisa só.
func TestConcurrentDemotionsKeepAFullAdmin(t *testing.T) {
	for nome, rebaixar := range map[string]func(f *fixture, svc *auth.Service, id int64) error{
		"rebaixar": func(_ *fixture, svc *auth.Service, id int64) error {
			_, err := svc.SetAccess(context.Background(), id, domain.RoleViewer, nil)
			return err
		},
		"apagar": func(_ *fixture, svc *auth.Service, id int64) error {
			return svc.DeleteUser(context.Background(), id)
		},
	} {
		t.Run(nome, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			a := f.withAdmin(t)
			b, err := f.svc.CreateUser(ctx, "bia", goodPassword, domain.RoleAdmin, domain.Scope{})
			if err != nil {
				t.Fatal(err)
			}

			var errB error
			svc := f.servicoIntercalado(func() { errB = rebaixar(f, f.svc, b.ID) })
			errA := rebaixar(f, svc, a.ID)

			if errA == nil && errB == nil {
				t.Fatal("os dois administradores saíram; a instalação ficou sem escopo total")
			}
			if !errors.Is(errA, auth.ErrLastAdmin) && !errors.Is(errB, auth.ErrLastAdmin) {
				t.Errorf("recusa sem ErrLastAdmin: %v / %v", errA, errB)
			}
		})
	}
}
