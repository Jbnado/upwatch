package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

func TestLoginWaitsForFloorForEveryPasswordOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
		want                     error
	}{
		{"inexistente", "ninguem", "senha-errada-porem-longa", auth.ErrInvalidCredentials},
		{"senha errada", "admin", "senha-errada-porem-longa", auth.ErrInvalidCredentials},
		{"senha certa", "admin", goodPassword, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.withAdmin(t)
			// A conta conserva o custo antigo; o disfarce nasce no novo.
			previous := domain.PasswordHashCost
			domain.PasswordHashCost = previous + 2
			t.Cleanup(func() { domain.PasswordHashCost = previous })
			const floor = 50 * time.Millisecond
			s := auth.New(f.store, auth.Options{Clock: f.clock, LoginFloor: floor})
			start := time.Now()
			_, err := s.Login(context.Background(), tc.username, tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Login = %v, esperado %v", err, tc.want)
			}
			if elapsed := time.Since(start); elapsed < floor {
				t.Fatalf("resposta em %s, antes do piso %s", elapsed, floor)
			}
		})
	}
}

func TestLoginFloorStopsOnContextCancellation(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	s := auth.New(f.store, auth.Options{Clock: f.clock, LoginFloor: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := s.Login(ctx, "admin", goodPassword)
	if !errors.Is(err, context.DeadlineExceeded) || result.Session != "" {
		t.Fatalf("login cancelado = %+v, %v; esperado prazo vencido sem sessão no retorno", result, err)
	}
}

func TestLoginRehashesOnlyAfterCorrectPassword(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	previous := domain.PasswordHashCost
	domain.PasswordHashCost = previous + 1
	t.Cleanup(func() { domain.PasswordHashCost = previous })
	if _, err := f.svc.Login(context.Background(), "admin", "senha-errada-porem-longa"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatal(err)
	}
	unchanged, err := f.store.Users().Get(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.PasswordHash != u.PasswordHash {
		t.Fatal("senha errada regravou o hash")
	}
	if _, err = f.svc.Login(context.Background(), "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.Users().Get(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(updated.PasswordHash))
	if err != nil || cost != domain.PasswordHashCost || !updated.CheckPassword(goodPassword) {
		t.Fatalf("hash não atualizado: custo %d, erro %v", cost, err)
	}
}

type rehashInterleaveStore struct {
	store.Store
	before func()
}

func (s *rehashInterleaveStore) Users() store.UserRepo {
	return &rehashInterleaveUsers{UserRepo: s.Store.Users(), before: s.before}
}

type rehashInterleaveUsers struct {
	store.UserRepo
	before func()
}

func (u *rehashInterleaveUsers) SetPasswordHashIf(ctx context.Context, id int64, expected, next string) error {
	u.before()
	return u.UserRepo.SetPasswordHashIf(ctx, id, expected, next)
}

func TestLoginRehashDoesNotOverwriteConcurrentPasswordChange(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	previous := domain.PasswordHashCost
	domain.PasswordHashCost = previous + 1
	t.Cleanup(func() { domain.PasswordHashCost = previous })
	changed := false
	st := &rehashInterleaveStore{Store: f.store, before: func() {
		changed = true
		if err := u.SetPassword("outra-senha-bem-longa"); err != nil {
			t.Fatal(err)
		}
		if err := f.store.Users().SetPasswordHash(context.Background(), u.ID, u.PasswordHash); err != nil {
			t.Fatal(err)
		}
	}}
	s := auth.New(st, auth.Options{Clock: f.clock})
	if _, err := s.Login(context.Background(), "admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Users().Get(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !got.CheckPassword("outra-senha-bem-longa") || got.CheckPassword(goodPassword) {
		t.Fatal("a regravação ignorou a troca concorrente da senha")
	}
}
