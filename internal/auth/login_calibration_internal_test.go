package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

func TestLoginCalibrationCoversStoredHashCost(t *testing.T) {
	s, fake := newInternalService(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("uma-senha-bem-longa"), domain.PasswordHashCost+2)
	if err != nil {
		t.Fatal(err)
	}
	u := domain.User{Username: "admin", Role: domain.RoleAdmin, PasswordHash: string(hash)}
	if err = s.store.Users().Create(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	calibrated := New(s.store, Options{Clock: fake})
	if err := calibrated.CalibrateLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(calibrated.decoy))
	if err != nil || cost < domain.PasswordHashCost+2 {
		t.Fatalf("disfarce custo %d, erro %v: hash armazenado mais caro ficou fora da calibração", cost, err)
	}
	if calibrated.loginFloor < 200*time.Millisecond || calibrated.loginFloor > 3*time.Second {
		t.Fatalf("piso fora dos limites: %s", calibrated.loginFloor)
	}
}

func TestLoginCalibrationRejectsUnsupportedStoredCost(t *testing.T) {
	s, fake := newInternalService(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("uma-senha-bem-longa"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	// O cabeçalho permite testar a guarda sem gastar CPU com um hash de custo 15.
	hash = []byte(strings.Replace(string(hash), "$04$", "$15$", 1))
	u := domain.User{Username: "admin", Role: domain.RoleAdmin, PasswordHash: string(hash)}
	if err := s.store.Users().Create(context.Background(), &u); err != nil {
		t.Fatal(err)
	}
	calibrated := New(s.store, Options{Clock: fake})
	if err := calibrated.CalibrateLogin(context.Background()); err == nil || !strings.Contains(err.Error(), "acima do máximo") {
		t.Fatalf("calibração com hash de custo 15: %v", err)
	}
}
