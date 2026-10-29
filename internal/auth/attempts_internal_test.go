package auth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
	"github.com/Jbnado/upwatch/internal/testdir"
)

var internalEpoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func newInternalService(t *testing.T) (*Service, *clock.Fake) {
	t.Helper()
	st, err := sqlstore.OpenSQLite(filepath.Join(testdir.New(t), "upwatch.db"), secretbox.Ephemeral())
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fake := clock.NewFake(internalEpoch)
	return New(st, Options{Clock: fake}), fake
}

func (a *attempts) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.records)
}

// Nome grande demais não chega a ocupar registro.
func TestOversizedUsernameIsNotTracked(t *testing.T) {
	s, _ := newInternalService(t)

	for i := range 50 {
		name := fmt.Sprintf("%03d", i) + strings.Repeat("a", 256<<10)
		if _, err := s.Login(context.Background(), name, "x"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Login = %v, want ErrInvalidCredentials", err)
		}
	}
	if n := s.attempts.size(); n != 0 {
		t.Errorf("registros = %d, want 0", n)
	}
}

// Registros de nomes inventados somem depois de uma janela.
func TestStaleRecordsAreSwept(t *testing.T) {
	s, fake := newInternalService(t)
	ctx := context.Background()

	for i := range 200 {
		_, _ = s.Login(ctx, fmt.Sprintf("ninguem-%03d", i), "senha-errada-porem-longa")
	}
	if n := s.attempts.size(); n != 200 {
		t.Fatalf("registros = %d, want 200", n)
	}

	fake.Advance(LockoutWindow + attemptSweepInterval)
	_, _ = s.Login(ctx, "mais-um", "senha-errada-porem-longa")

	if n := s.attempts.size(); n != 1 {
		t.Errorf("registros depois da varredura = %d, want 1", n)
	}
}

// O mapa não passa do teto, mesmo com nomes novos sem parar.
func TestTrackedAccountsAreCapped(t *testing.T) {
	a := newAttempts(clock.NewFake(internalEpoch))

	for i := range maxTrackedAccounts + 100 {
		att, err := a.reserve(fmt.Sprintf("conta-%d", i))
		if err != nil {
			t.Fatalf("reserve #%d = %v, want nil", i, err)
		}
		att.fail()
	}
	if n := a.size(); n > maxTrackedAccounts {
		t.Errorf("registros = %d, want no máximo %d", n, maxTrackedAccounts)
	}
}

// Com o mapa cheio só de contas bloqueadas, nenhuma é esquecida: a
// tentativa nova é recusada.
func TestFullMapOfLockedAccountsRefuses(t *testing.T) {
	fake := clock.NewFake(internalEpoch)
	a := newAttempts(fake)
	a.records = make(map[attemptKey]*attemptRecord, maxTrackedAccounts)
	for i := range maxTrackedAccounts {
		a.records[keyFor(fmt.Sprintf("conta-%d", i))] = &attemptRecord{
			failures: MaxLoginAttempts, lastFailure: fake.Now(), lockedTill: fake.Now().Add(LockoutWindow),
		}
	}
	a.lastSweep = fake.Now()

	if _, err := a.reserve("nova"); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("reserve = %v, want ErrTooManyAttempts", err)
	}
	if n := a.size(); n != maxTrackedAccounts {
		t.Errorf("registros = %d, want %d", n, maxTrackedAccounts)
	}
}

// Conta inexistente passa por um bcrypt de verdade, no custo configurado:
// é o que iguala o tempo de resposta ao de uma senha errada.
func TestUnknownUserRunsBcryptAtConfiguredCost(t *testing.T) {
	old := domain.PasswordHashCost
	domain.PasswordHashCost = bcrypt.MinCost + 1
	t.Cleanup(func() { domain.PasswordHashCost = old })

	s, _ := newInternalService(t)
	if _, err := s.Login(context.Background(), "ninguem", "senha-errada-porem-longa"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login = %v, want ErrInvalidCredentials", err)
	}

	cost, err := bcrypt.Cost([]byte(s.decoy))
	if err != nil {
		t.Fatalf("hash de disfarce inválido (%q): %v", s.decoy, err)
	}
	if cost != domain.PasswordHashCost {
		t.Errorf("custo do disfarce = %d, want %d", cost, domain.PasswordHashCost)
	}
}
