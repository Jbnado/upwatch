package storetest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// twoFactorCases cobre o segundo fator: segredo, códigos de recuperação e
// desafios de login.
func twoFactorCases() []conformanceCase {
	return []conformanceCase{
		{"TwoFactorPendingRoundTrips", testTwoFactorPendingRoundTrips},
		{"TwoFactorSecretIsCipheredAtRest", testTwoFactorSecretIsCipheredAtRest},
		{"TwoFactorPendingIsReplaced", testTwoFactorPendingIsReplaced},
		{"TwoFactorEnableShowsOnTheUser", testTwoFactorEnableShowsOnTheUser},
		{"TwoFactorSetPendingRefusesEnabled", testTwoFactorSetPendingRefusesEnabled},
		{"TwoFactorEnableNeedsPending", testTwoFactorEnableNeedsPending},
		{"TwoFactorAdvanceStepRejectsReplay", testTwoFactorAdvanceStepRejectsReplay},
		{"TwoFactorConcurrentStepAcceptsOne", testTwoFactorConcurrentStepAcceptsOne},
		{"TwoFactorRecoveryCodeIsSingleUse", testTwoFactorRecoveryCodeIsSingleUse},
		{"TwoFactorRecoveryCodesAreReplaced", testTwoFactorRecoveryCodesAreReplaced},
		{"TwoFactorDisableRemovesEverything", testTwoFactorDisableRemovesEverything},
		{"TwoFactorCascadesOnUserDelete", testTwoFactorCascadesOnUserDelete},

		{"ChallengeCountsAttemptsUpToTheCap", testChallengeCountsAttemptsUpToTheCap},
		{"ChallengeExpires", testChallengeExpires},
		{"ChallengeIsConsumedOnce", testChallengeIsConsumedOnce},
		{"ChallengeDeleteExpiredKeepsValidOnes", testChallengeDeleteExpiredKeepsValidOnes},

		{"SessionDeleteOthersKeepsTheCurrent", testSessionDeleteOthersKeepsTheCurrent},
	}
}

const testTOTPSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

func mustEnableTOTP(t *testing.T, s store.Store, userID int64, codes ...string) {
	t.Helper()
	ctx := context.Background()
	if err := s.TwoFactor().SetPending(ctx, userID, testTOTPSecret, epoch); err != nil {
		t.Fatalf("SetPending returned unexpected error: %v", err)
	}
	if err := s.TwoFactor().Enable(ctx, userID, 100, codes, epoch); err != nil {
		t.Fatalf("Enable returned unexpected error: %v", err)
	}
}

func testTwoFactorPendingRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	if _, err := s.TwoFactor().Get(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get antes do cadastro error = %v, want ErrNotFound", err)
	}
	if err := s.TwoFactor().SetPending(ctx, u.ID, testTOTPSecret, epoch); err != nil {
		t.Fatalf("SetPending returned unexpected error: %v", err)
	}

	got, err := s.TwoFactor().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Secret != testTOTPSecret || got.Enabled || got.LastStep != 0 || !got.CreatedAt.Equal(epoch) {
		t.Errorf("Get = %+v, want pendente com o segredo gravado", got)
	}
	// Pendente ainda não vale: a conta não aparece como protegida.
	if acc, _ := s.Users().Get(ctx, u.ID); acc.TOTPEnabled {
		t.Error("TOTPEnabled = true com cadastro só pendente")
	}
}

// O segredo TOTP precisa ficar cifrado em repouso, como as credenciais
// dos monitores: o backup do banco não pode carregar o segundo fator.
func testTwoFactorSecretIsCipheredAtRest(t *testing.T, newStore Factory) {
	s := newStore(t)
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID)

	raw, ok := s.(rawDB)
	if !ok {
		t.Fatalf("o store %T não expõe DB()", s)
	}
	var stored string
	if err := raw.DB().QueryRowContext(context.Background(),
		`SELECT secret FROM user_totp`).Scan(&stored); err != nil {
		t.Fatalf("lendo user_totp returned unexpected error: %v", err)
	}
	if strings.Contains(stored, testTOTPSecret) || !strings.HasPrefix(stored, "enc:") {
		t.Errorf("secret em repouso = %q, want cifrado", stored)
	}
}

func testTwoFactorPendingIsReplaced(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	_ = s.TwoFactor().SetPending(ctx, u.ID, "AAAAAAAAAAAAAAAA", epoch)
	if err := s.TwoFactor().SetPending(ctx, u.ID, testTOTPSecret, epoch.Add(time.Minute)); err != nil {
		t.Fatalf("segundo SetPending returned unexpected error: %v", err)
	}
	got, _ := s.TwoFactor().Get(ctx, u.ID)
	if got.Secret != testTOTPSecret {
		t.Errorf("Secret = %q, want o do último cadastro", got.Secret)
	}
}

func testTwoFactorEnableShowsOnTheUser(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustCreateUser(t, s, "outra")
	mustEnableTOTP(t, s, u.ID, "h1", "h2")

	got, _ := s.TwoFactor().Get(ctx, u.ID)
	if !got.Enabled || got.LastStep != 100 || got.EnabledAt == nil {
		t.Errorf("Get = %+v, want ligado no passo 100", got)
	}
	for _, read := range []func() (domain.User, error){
		func() (domain.User, error) { return s.Users().Get(ctx, u.ID) },
		func() (domain.User, error) { return s.Users().GetByUsername(ctx, "admin") },
	} {
		acc, err := read()
		if err != nil {
			t.Fatalf("lendo a conta returned unexpected error: %v", err)
		}
		if !acc.TOTPEnabled {
			t.Error("TOTPEnabled = false depois de ligar")
		}
	}
	all, _ := s.Users().List(ctx)
	for _, acc := range all {
		if want := acc.ID == u.ID; acc.TOTPEnabled != want {
			t.Errorf("List: %s TOTPEnabled = %v, want %v", acc.Username, acc.TOTPEnabled, want)
		}
	}
}

func testTwoFactorSetPendingRefusesEnabled(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID)

	err := s.TwoFactor().SetPending(ctx, u.ID, "AAAAAAAAAAAAAAAA", epoch)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("SetPending com fator ligado error = %v, want ErrConflict", err)
	}
	got, _ := s.TwoFactor().Get(ctx, u.ID)
	if got.Secret != testTOTPSecret || !got.Enabled {
		t.Errorf("Get = %+v, want o fator ligado intacto", got)
	}
}

func testTwoFactorEnableNeedsPending(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	if err := s.TwoFactor().Enable(ctx, u.ID, 1, nil, epoch); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Enable sem pendente error = %v, want ErrNotFound", err)
	}
	mustEnableTOTP(t, s, u.ID)
	if err := s.TwoFactor().Enable(ctx, u.ID, 200, nil, epoch); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Enable de fator já ligado error = %v, want ErrNotFound", err)
	}
}

func testTwoFactorAdvanceStepRejectsReplay(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID)

	for _, tc := range []struct {
		step int64
		want bool
	}{{100, false}, {99, false}, {101, true}, {101, false}, {105, true}} {
		ok, err := s.TwoFactor().AdvanceStep(ctx, u.ID, tc.step)
		if err != nil {
			t.Fatalf("AdvanceStep returned unexpected error: %v", err)
		}
		if ok != tc.want {
			t.Errorf("AdvanceStep(%d) = %v, want %v", tc.step, ok, tc.want)
		}
	}

	// Pendente não aceita passo: só o fator confirmado vale para login.
	other := mustCreateUser(t, s, "outra")
	_ = s.TwoFactor().SetPending(ctx, other.ID, testTOTPSecret, epoch)
	if ok, _ := s.TwoFactor().AdvanceStep(ctx, other.ID, 500); ok {
		t.Error("AdvanceStep aceitou passo de cadastro pendente")
	}
}

// Dois pedidos com o mesmo código ao mesmo tempo: só um passa.
func testTwoFactorConcurrentStepAcceptsOne(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.TwoFactor().AdvanceStep(ctx, u.ID, 101)
			if err != nil {
				t.Errorf("AdvanceStep returned unexpected error: %v", err)
				return
			}
			if ok {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Errorf("%d pedidos aceitos com o mesmo passo, want 1", accepted)
	}
}

func testTwoFactorRecoveryCodeIsSingleUse(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "outra")
	mustEnableTOTP(t, s, u.ID, "h1", "h2", "h3")

	if n, _ := s.TwoFactor().RecoveryCodesLeft(ctx, u.ID); n != 3 {
		t.Errorf("RecoveryCodesLeft = %d, want 3", n)
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, other.ID, "h1", epoch); ok {
		t.Error("código de uma conta aceito em outra")
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, u.ID, "h1", epoch); !ok {
		t.Fatal("primeiro uso do código recusado")
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, u.ID, "h1", epoch); ok {
		t.Error("código de recuperação aceito duas vezes")
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, u.ID, "inexistente", epoch); ok {
		t.Error("código desconhecido aceito")
	}
	if n, _ := s.TwoFactor().RecoveryCodesLeft(ctx, u.ID); n != 2 {
		t.Errorf("RecoveryCodesLeft = %d, want 2", n)
	}
}

func testTwoFactorRecoveryCodesAreReplaced(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID, "h1", "h2")

	if err := s.TwoFactor().ReplaceRecoveryCodes(ctx, u.ID, []string{"n1", "n2", "n3"}); err != nil {
		t.Fatalf("ReplaceRecoveryCodes returned unexpected error: %v", err)
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, u.ID, "h1", epoch); ok {
		t.Error("código antigo seguiu valendo depois da troca")
	}
	if ok, _ := s.TwoFactor().UseRecoveryCode(ctx, u.ID, "n2", epoch); !ok {
		t.Error("código novo recusado")
	}
}

func testTwoFactorDisableRemovesEverything(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	mustEnableTOTP(t, s, u.ID, "h1")

	if err := s.TwoFactor().Disable(ctx, u.ID); err != nil {
		t.Fatalf("Disable returned unexpected error: %v", err)
	}
	if _, err := s.TwoFactor().Get(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get depois de Disable error = %v, want ErrNotFound", err)
	}
	if n, _ := s.TwoFactor().RecoveryCodesLeft(ctx, u.ID); n != 0 {
		t.Errorf("RecoveryCodesLeft = %d depois de Disable, want 0", n)
	}
	if acc, _ := s.Users().Get(ctx, u.ID); acc.TOTPEnabled {
		t.Error("TOTPEnabled = true depois de Disable")
	}
	if err := s.TwoFactor().Disable(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("segundo Disable error = %v, want ErrNotFound", err)
	}
}

func testTwoFactorCascadesOnUserDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	// Sobra outro administrador: a conta apagada não pode ser a última.
	mustCreateUser(t, s, "admin")
	u := mustCreateUser(t, s, "sai")
	mustEnableTOTP(t, s, u.ID, "h1")
	_ = s.TwoFactor().CreateChallenge(ctx, domain.LoginChallenge{
		Hash: "c1", UserID: u.ID, CreatedAt: epoch, ExpiresAt: epoch.Add(time.Hour),
	})

	if err := s.Users().Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	if _, err := s.TwoFactor().Get(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get depois de apagar a conta error = %v, want ErrNotFound", err)
	}
	if _, err := s.TwoFactor().AttemptChallenge(ctx, "c1", epoch, 5); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("desafio sobreviveu à conta: error = %v, want ErrNotFound", err)
	}
}

// ---------- desafios ----------

func newChallenge(userID int64, hash string, expires time.Time) domain.LoginChallenge {
	return domain.LoginChallenge{Hash: hash, UserID: userID, CreatedAt: epoch, ExpiresAt: expires}
}

func testChallengeCountsAttemptsUpToTheCap(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	if err := s.TwoFactor().CreateChallenge(ctx, newChallenge(u.ID, "c1", epoch.Add(5*time.Minute))); err != nil {
		t.Fatalf("CreateChallenge returned unexpected error: %v", err)
	}

	for i := 1; i <= 3; i++ {
		c, err := s.TwoFactor().AttemptChallenge(ctx, "c1", epoch, 3)
		if err != nil {
			t.Fatalf("tentativa %d returned unexpected error: %v", i, err)
		}
		if c.Attempts != i || c.UserID != u.ID {
			t.Errorf("tentativa %d = %+v, want Attempts=%d da conta %d", i, c, i, u.ID)
		}
	}
	if _, err := s.TwoFactor().AttemptChallenge(ctx, "c1", epoch, 3); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tentativa além do limite error = %v, want ErrNotFound", err)
	}
	if _, err := s.TwoFactor().AttemptChallenge(ctx, "outro", epoch, 3); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("desafio desconhecido error = %v, want ErrNotFound", err)
	}
}

func testChallengeExpires(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	expires := epoch.Add(5 * time.Minute)
	_ = s.TwoFactor().CreateChallenge(ctx, newChallenge(u.ID, "c1", expires))

	if _, err := s.TwoFactor().AttemptChallenge(ctx, "c1", expires, 5); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tentativa no vencimento error = %v, want ErrNotFound", err)
	}
}

func testChallengeIsConsumedOnce(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	_ = s.TwoFactor().CreateChallenge(ctx, newChallenge(u.ID, "c1", epoch.Add(time.Hour)))

	if err := s.TwoFactor().ConsumeChallenge(ctx, "c1"); err != nil {
		t.Fatalf("ConsumeChallenge returned unexpected error: %v", err)
	}
	if err := s.TwoFactor().ConsumeChallenge(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("segundo ConsumeChallenge error = %v, want ErrNotFound", err)
	}
	if _, err := s.TwoFactor().AttemptChallenge(ctx, "c1", epoch, 5); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tentativa em desafio consumido error = %v, want ErrNotFound", err)
	}
}

func testChallengeDeleteExpiredKeepsValidOnes(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	_ = s.TwoFactor().CreateChallenge(ctx, newChallenge(u.ID, "velho", epoch.Add(-time.Minute)))
	_ = s.TwoFactor().CreateChallenge(ctx, newChallenge(u.ID, "novo", epoch.Add(time.Minute)))

	n, err := s.TwoFactor().DeleteExpiredChallenges(ctx, epoch)
	if err != nil {
		t.Fatalf("DeleteExpiredChallenges returned unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteExpiredChallenges removeu %d, want 1", n)
	}
	if _, err := s.TwoFactor().AttemptChallenge(ctx, "novo", epoch, 5); err != nil {
		t.Errorf("desafio válido sumiu: %v", err)
	}
}

func testSessionDeleteOthersKeepsTheCurrent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "outra")
	expires := epoch.Add(time.Hour)
	for _, sess := range []domain.Session{
		newSession(u.ID, "atual", expires),
		newSession(u.ID, "velha", expires),
		newSession(other.ID, "alheia", expires),
	} {
		if err := s.Sessions().Create(ctx, sess); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
	}

	if err := s.Sessions().DeleteOthers(ctx, u.ID, "atual"); err != nil {
		t.Fatalf("DeleteOthers returned unexpected error: %v", err)
	}
	if _, err := s.Sessions().Get(ctx, "atual"); err != nil {
		t.Errorf("a sessão atual sumiu: %v", err)
	}
	if _, err := s.Sessions().Get(ctx, "velha"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a outra sessão sobreviveu: error = %v, want ErrNotFound", err)
	}
	if _, err := s.Sessions().Get(ctx, "alheia"); err != nil {
		t.Errorf("a sessão de outra conta sumiu: %v", err)
	}
}
