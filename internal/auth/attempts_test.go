package auth_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Jbnado/upwatch/internal/auth"
)

// parallel roda fn n vezes ao mesmo tempo e conta os erros iguais a want.
func parallel(n int, want error, fn func(i int) error) int {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		count int
	)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if errors.Is(fn(i), want) {
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	return count
}

// Senhas erradas em paralelo não passam do limite: a tentativa é
// reservada antes da conferência, e as reservas contam.
func TestParallelLoginsAreLimitedToTheThreshold(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	checked := parallel(60, auth.ErrInvalidCredentials, func(i int) error {
		_, err := f.svc.Login(ctx, "admin", fmt.Sprintf("palpite-numero-%03d", i))
		return err
	})

	if checked > auth.MaxLoginAttempts {
		t.Errorf("%d senhas conferidas em paralelo, want no máximo %d", checked, auth.MaxLoginAttempts)
	}
	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("Login depois da rajada = %v, want ErrTooManyAttempts", err)
	}
}

// O mesmo vale para códigos espalhados por vários desafios.
func TestParallelCodesAreLimitedToTheThreshold(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	var challenges []string
	for range 12 {
		challenges = append(challenges, f.challenge(t))
	}
	wrong := wrongCode(f.code(t, e.secret))

	checked := parallel(len(challenges)*auth.MaxChallengeAttempts, auth.ErrInvalidCode, func(i int) error {
		_, err := f.svc.CompleteLogin(ctx, challenges[i%len(challenges)], wrong)
		return err
	})

	if checked > auth.MaxLoginAttempts {
		t.Errorf("%d códigos conferidos em paralelo, want no máximo %d", checked, auth.MaxLoginAttempts)
	}
}

// E para a prova de senha e código das ações do segundo fator.
func TestParallelProofsAreLimitedToTheThreshold(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	checked := parallel(40, auth.ErrInvalidCredentials, func(i int) error {
		_, err := f.svc.RegenerateRecoveryCodes(ctx, e.user.ID, fmt.Sprintf("palpite-numero-%03d", i), "000000")
		return err
	})

	if checked > auth.MaxLoginAttempts {
		t.Errorf("%d provas conferidas em paralelo, want no máximo %d", checked, auth.MaxLoginAttempts)
	}
}

// A senha atual errada na troca de senha conta no bloqueio.
func TestChangePasswordCountsTowardLockout(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	for i := range auth.MaxLoginAttempts {
		err := f.svc.ChangePassword(ctx, admin.ID, fmt.Sprintf("palpite-numero-%03d", i), "nova-senha-bem-longa")
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("ChangePassword #%d = %v, want ErrInvalidCredentials", i+1, err)
		}
	}

	if err := f.svc.ChangePassword(ctx, admin.ID, goodPassword, "nova-senha-bem-longa"); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("ChangePassword com a senha certa depois do limite = %v, want ErrTooManyAttempts", err)
	}
	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("Login depois do limite = %v, want ErrTooManyAttempts", err)
	}
}

// Começar o cadastro do segundo fator pede a senha, e a errada conta.
func TestBeginTOTPNeedsThePassword(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	for i := range auth.MaxLoginAttempts {
		if _, err := f.svc.BeginTOTP(ctx, admin.ID, fmt.Sprintf("palpite-numero-%03d", i)); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("BeginTOTP #%d = %v, want ErrInvalidCredentials", i+1, err)
		}
	}
	st, err := f.svc.TOTPStatus(ctx, admin.ID)
	if err != nil {
		t.Fatalf("TOTPStatus returned unexpected error: %v", err)
	}
	if st.Pending {
		t.Error("cadastro pendente depois de senhas erradas, want nenhum")
	}

	if _, err := f.svc.BeginTOTP(ctx, admin.ID, goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("BeginTOTP com a senha certa depois do limite = %v, want ErrTooManyAttempts", err)
	}
}

// Falhas abaixo do limite caducam depois de uma janela sem erro novo.
func TestFailuresBelowThresholdDecay(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	for range auth.MaxLoginAttempts - 1 {
		if _, err := f.svc.Login(ctx, "admin", "senha-errada-porem-longa"); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("Login = %v, want ErrInvalidCredentials", err)
		}
	}
	f.clock.Advance(auth.LockoutWindow)

	if _, err := f.svc.Login(ctx, "admin", "senha-errada-porem-longa"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login = %v, want ErrInvalidCredentials", err)
	}
	if _, err := f.svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Errorf("Login com a senha certa = %v, want nil: as falhas antigas deviam ter caducado", err)
	}
}

// Nome maior que o aceito no cadastro é recusado como credencial errada.
func TestLoginRejectsOversizedUsername(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	_, err := f.svc.Login(context.Background(), strings.Repeat("a", 65), goodPassword)
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("Login = %v, want ErrInvalidCredentials", err)
	}
}
