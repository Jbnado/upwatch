package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/totp"
)

// enrolled é uma conta com o segundo fator ligado.
type enrolled struct {
	user     domain.User
	secret   string
	recovery []string
}

// withTOTP cria o administrador e liga o segundo fator dele.
func (f *fixture) withTOTP(t *testing.T) enrolled {
	t.Helper()
	ctx := context.Background()
	u := f.withAdmin(t)

	e, err := f.svc.BeginTOTP(ctx, u.ID)
	if err != nil {
		t.Fatalf("BeginTOTP returned unexpected error: %v", err)
	}
	codes, err := f.svc.ConfirmTOTP(ctx, u.ID, "", f.code(t, e.Secret))
	if err != nil {
		t.Fatalf("ConfirmTOTP returned unexpected error: %v", err)
	}
	// O código da confirmação já foi usado; o próximo precisa de outro
	// passo.
	f.clock.Advance(totp.Period)
	return enrolled{user: u, secret: e.Secret, recovery: codes}
}

// code é o código do aplicativo agora.
func (f *fixture) code(t *testing.T, secret string) string {
	t.Helper()
	c, err := totp.Code(secret, f.clock.Now())
	if err != nil {
		t.Fatalf("totp.Code returned unexpected error: %v", err)
	}
	return c
}

// challenge faz o primeiro passo do login e devolve o desafio.
func (f *fixture) challenge(t *testing.T) string {
	t.Helper()
	res, err := f.svc.Login(context.Background(), "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if !res.NeedsSecondFactor() {
		t.Fatal("Login não pediu o segundo fator de uma conta com ele ligado")
	}
	return res.Challenge
}

// wrongCode é um código de seis dígitos que não é o atual.
func wrongCode(right string) string {
	if right == "000000" {
		return "111111"
	}
	return "000000"
}

// A senha certa de uma conta com segundo fator não abre sessão: devolve
// só o desafio, e nenhuma sessão nasce no banco.
func TestLoginWithTOTPOpensNoSession(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	res, err := f.svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if res.Session != "" {
		t.Errorf("Login devolveu sessão para conta com segundo fator")
	}
	if !res.NeedsSecondFactor() || !res.ChallengeExpiresAt.Equal(f.clock.Now().Add(auth.ChallengeTTL)) {
		t.Errorf("Login = %+v, want desafio que vence em %v", res, auth.ChallengeTTL)
	}
	// O desafio não serve de cookie.
	if _, err := f.svc.AuthenticateSession(ctx, res.Challenge); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("o desafio autenticou como sessão: error = %v", err)
	}
	var sessions int
	if err := f.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session WHERE user_id = ?`, e.user.ID).Scan(&sessions); err != nil {
		t.Fatalf("contando sessões returned unexpected error: %v", err)
	}
	if sessions != 0 {
		t.Errorf("%d sessões abertas antes do código, want 0", sessions)
	}
}

// Senha errada responde igual com ou sem segundo fator.
func TestWrongPasswordDoesNotRevealTOTP(t *testing.T) {
	f := newFixture(t)
	f.withTOTP(t)

	res, err := f.svc.Login(context.Background(), "admin", "senha-errada-porem-longa")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
	if res.NeedsSecondFactor() {
		t.Error("senha errada devolveu desafio")
	}
}

func TestCompleteLoginOpensSession(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	secret, err := f.svc.CompleteLogin(ctx, f.challenge(t), f.code(t, e.secret))
	if err != nil {
		t.Fatalf("CompleteLogin returned unexpected error: %v", err)
	}
	u, err := f.svc.AuthenticateSession(ctx, secret)
	if err != nil || u.ID != e.user.ID {
		t.Errorf("AuthenticateSession = %v, %v; want a conta %d", u, err, e.user.ID)
	}
}

// O mesmo código não abre duas sessões, nem em desafios diferentes.
func TestCompleteLoginRejectsReplayedCode(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()
	code := f.code(t, e.secret)

	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), code); err != nil {
		t.Fatalf("primeiro uso returned unexpected error: %v", err)
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), code); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("código repetido error = %v, want ErrInvalidCode", err)
	}
}

func TestChallengeIsSingleUse(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()
	c := f.challenge(t)

	if _, err := f.svc.CompleteLogin(ctx, c, f.code(t, e.secret)); err != nil {
		t.Fatalf("CompleteLogin returned unexpected error: %v", err)
	}
	f.clock.Advance(totp.Period)
	if _, err := f.svc.CompleteLogin(ctx, c, f.code(t, e.secret)); !errors.Is(err, auth.ErrChallengeInvalid) {
		t.Errorf("desafio reusado error = %v, want ErrChallengeInvalid", err)
	}
}

func TestChallengeExpires(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	c := f.challenge(t)

	f.clock.Advance(auth.ChallengeTTL)
	if _, err := f.svc.CompleteLogin(context.Background(), c, f.code(t, e.secret)); !errors.Is(err, auth.ErrChallengeInvalid) {
		t.Errorf("desafio vencido error = %v, want ErrChallengeInvalid", err)
	}
}

// Esgotadas as tentativas, nem o código certo serve mais: o desafio
// morreu, e é preciso voltar à senha.
func TestChallengeAttemptCap(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()
	c := f.challenge(t)

	// Quatro erros só: o quinto acionaria o bloqueio da conta, que é
	// outro teste.
	for range auth.MaxChallengeAttempts - 1 {
		if _, err := f.svc.CompleteLogin(ctx, c, wrongCode(f.code(t, e.secret))); !errors.Is(err, auth.ErrInvalidCode) {
			t.Fatalf("código errado error = %v, want ErrInvalidCode", err)
		}
	}
	if _, err := f.svc.CompleteLogin(ctx, c, "não-é-código"); !errors.Is(err, auth.ErrInvalidCode) {
		t.Fatalf("quinta tentativa error = %v, want ErrInvalidCode", err)
	}
	// O bloqueio por conta também chegou ao limite; o que importa aqui é
	// que o desafio em si já não aceita nada.
	if _, err := f.svc.CompleteLogin(ctx, c, f.code(t, e.secret)); !errors.Is(err, auth.ErrChallengeInvalid) {
		t.Errorf("tentativa além do limite error = %v, want ErrChallengeInvalid", err)
	}
}

// Código errado conta no bloqueio da conta, como senha errada.
func TestWrongCodesCountTowardLockout(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	c := f.challenge(t)
	for range auth.MaxLoginAttempts {
		_, _ = f.svc.CompleteLogin(ctx, c, wrongCode(f.code(t, e.secret)))
	}

	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("Login depois de %d códigos errados error = %v, want ErrTooManyAttempts",
			auth.MaxLoginAttempts, err)
	}
}

// Acertar a senha não zera as falhas de código: senão bastaria digitar a
// senha de novo a cada poucos palpites.
func TestCorrectPasswordDoesNotResetCodeFailures(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	for range auth.MaxLoginAttempts - 1 {
		_, _ = f.svc.CompleteLogin(ctx, f.challenge(t), wrongCode(f.code(t, e.secret)))
	}
	_, _ = f.svc.CompleteLogin(ctx, f.challenge(t), wrongCode(f.code(t, e.secret)))

	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("error = %v, want ErrTooManyAttempts", err)
	}
}

func TestRecoveryCodeLogsInOnce(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	if len(e.recovery) != auth.RecoveryCodeCount {
		t.Fatalf("%d códigos de recuperação, want %d", len(e.recovery), auth.RecoveryCodeCount)
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), e.recovery[0]); err != nil {
		t.Fatalf("login com código de recuperação returned unexpected error: %v", err)
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), e.recovery[0]); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("código de recuperação reusado error = %v, want ErrInvalidCode", err)
	}
	// Digitado sem hífens e em maiúsculas, o próximo vale igual.
	loose := ""
	for _, r := range e.recovery[1] {
		if r != '-' {
			loose += string(r)
		}
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), " "+toUpper(loose)+" "); err != nil {
		t.Errorf("código sem hífens returned unexpected error: %v", err)
	}
	st, _ := f.svc.TOTPStatus(ctx, e.user.ID)
	if st.RecoveryCodesLeft != auth.RecoveryCodeCount-2 {
		t.Errorf("RecoveryCodesLeft = %d, want %d", st.RecoveryCodesLeft, auth.RecoveryCodeCount-2)
	}
}

func toUpper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 'a' + 'A'
		}
	}
	return string(out)
}

// Ligar o segundo fator derruba as outras sessões e mantém a de quem
// ligou.
func TestEnablingTOTPRevokesOtherSessions(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	current := mustLogin(t, f)
	other := mustLogin(t, f)

	e, err := f.svc.BeginTOTP(ctx, u.ID)
	if err != nil {
		t.Fatalf("BeginTOTP returned unexpected error: %v", err)
	}
	if _, err := f.svc.ConfirmTOTP(ctx, u.ID, current, f.code(t, e.Secret)); err != nil {
		t.Fatalf("ConfirmTOTP returned unexpected error: %v", err)
	}

	if _, err := f.svc.AuthenticateSession(ctx, current); err != nil {
		t.Errorf("a sessão de quem ligou caiu: %v", err)
	}
	if _, err := f.svc.AuthenticateSession(ctx, other); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("a outra sessão sobreviveu: error = %v", err)
	}
}

func mustLogin(t *testing.T, f *fixture) string {
	t.Helper()
	res, err := f.svc.Login(context.Background(), "admin", goodPassword)
	if err != nil || res.Session == "" {
		t.Fatalf("Login = %+v, %v; want sessão", res, err)
	}
	return res.Session
}

func TestConfirmRejectsWrongCode(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	ctx := context.Background()

	e, _ := f.svc.BeginTOTP(ctx, u.ID)
	if _, err := f.svc.ConfirmTOTP(ctx, u.ID, "", wrongCode(f.code(t, e.Secret))); !errors.Is(err, auth.ErrInvalidCode) {
		t.Fatalf("error = %v, want ErrInvalidCode", err)
	}
	// Sem confirmação, o login segue só com a senha.
	if res, err := f.svc.Login(ctx, "admin", goodPassword); err != nil || res.NeedsSecondFactor() {
		t.Errorf("Login = %+v, %v; want sessão direta com cadastro só pendente", res, err)
	}
}

func TestBeginRefusesWhenEnabled(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)

	if _, err := f.svc.BeginTOTP(context.Background(), e.user.ID); !errors.Is(err, auth.ErrTOTPEnabled) {
		t.Errorf("error = %v, want ErrTOTPEnabled", err)
	}
}

func TestDisableNeedsPasswordAndCode(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	if err := f.svc.DisableTOTP(ctx, e.user.ID, "", "senha-errada-porem-longa", f.code(t, e.secret)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("senha errada error = %v, want ErrInvalidCredentials", err)
	}
	if err := f.svc.DisableTOTP(ctx, e.user.ID, "", goodPassword, wrongCode(f.code(t, e.secret))); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("código errado error = %v, want ErrInvalidCode", err)
	}
	if err := f.svc.DisableTOTP(ctx, e.user.ID, "", goodPassword, e.recovery[0]); err != nil {
		t.Fatalf("DisableTOTP returned unexpected error: %v", err)
	}
	if res, err := f.svc.Login(ctx, "admin", goodPassword); err != nil || res.NeedsSecondFactor() {
		t.Errorf("Login depois de desligar = %+v, %v; want sessão direta", res, err)
	}
}

func TestDisablingTOTPRevokesOtherSessions(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	current, _ := f.svc.CompleteLogin(ctx, f.challenge(t), f.code(t, e.secret))
	f.clock.Advance(totp.Period)
	other, _ := f.svc.CompleteLogin(ctx, f.challenge(t), f.code(t, e.secret))
	f.clock.Advance(totp.Period)

	if err := f.svc.DisableTOTP(ctx, e.user.ID, current, goodPassword, f.code(t, e.secret)); err != nil {
		t.Fatalf("DisableTOTP returned unexpected error: %v", err)
	}
	if _, err := f.svc.AuthenticateSession(ctx, current); err != nil {
		t.Errorf("a sessão de quem desligou caiu: %v", err)
	}
	if _, err := f.svc.AuthenticateSession(ctx, other); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("a outra sessão sobreviveu: error = %v", err)
	}
}

func TestRegenerateRecoveryCodesInvalidatesOldOnes(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	if _, err := f.svc.RegenerateRecoveryCodes(ctx, e.user.ID, goodPassword, wrongCode(f.code(t, e.secret))); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("código errado error = %v, want ErrInvalidCode", err)
	}
	fresh, err := f.svc.RegenerateRecoveryCodes(ctx, e.user.ID, goodPassword, f.code(t, e.secret))
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes returned unexpected error: %v", err)
	}
	if len(fresh) != auth.RecoveryCodeCount {
		t.Errorf("%d códigos novos, want %d", len(fresh), auth.RecoveryCodeCount)
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), e.recovery[1]); !errors.Is(err, auth.ErrInvalidCode) {
		t.Errorf("código antigo error = %v, want ErrInvalidCode", err)
	}
	if _, err := f.svc.CompleteLogin(ctx, f.challenge(t), fresh[0]); err != nil {
		t.Errorf("código novo returned unexpected error: %v", err)
	}
}

func TestAdminResetDisablesAndRevokesSessions(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()
	other, err := f.svc.CreateUser(ctx, "outra", goodPassword, domain.RoleAdmin, domain.Scope{})
	if err != nil {
		t.Fatalf("CreateUser returned unexpected error: %v", err)
	}
	session, _ := f.svc.CompleteLogin(ctx, f.challenge(t), f.code(t, e.secret))

	if err := f.svc.ResetTOTP(ctx, e.user.ID, e.user.ID); !errors.Is(err, auth.ErrResetOwnTOTP) {
		t.Errorf("reset da própria conta error = %v, want ErrResetOwnTOTP", err)
	}
	if err := f.svc.ResetTOTP(ctx, other.ID, e.user.ID); err != nil {
		t.Fatalf("ResetTOTP returned unexpected error: %v", err)
	}
	if _, err := f.svc.AuthenticateSession(ctx, session); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("a sessão da conta sobreviveu ao reset: error = %v", err)
	}
	if res, err := f.svc.Login(ctx, "admin", goodPassword); err != nil || res.NeedsSecondFactor() {
		t.Errorf("Login depois do reset = %+v, %v; want sessão direta", res, err)
	}
	if err := f.svc.ResetTOTP(ctx, other.ID, e.user.ID); !errors.Is(err, auth.ErrTOTPNotEnabled) {
		t.Errorf("segundo reset error = %v, want ErrTOTPNotEnabled", err)
	}
}

func TestResetByUsernameIsTheLastResort(t *testing.T) {
	f := newFixture(t)
	f.withTOTP(t)
	ctx := context.Background()

	if err := f.svc.ResetTOTPByUsername(ctx, "admin"); err != nil {
		t.Fatalf("ResetTOTPByUsername returned unexpected error: %v", err)
	}
	if err := f.svc.ResetTOTPByUsername(ctx, "ninguem"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("conta inexistente error = %v, want ErrNotFound", err)
	}
}

// Token de API não passa pelo segundo fator.
func TestTokenStillAuthenticatesWithTOTP(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ctx := context.Background()

	_, secret, err := f.svc.IssueToken(ctx, e.user.ID, "ci", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	if u, err := f.svc.AuthenticateToken(ctx, secret); err != nil || u.ID != e.user.ID {
		t.Errorf("AuthenticateToken = %v, %v; want a conta %d", u, err, e.user.ID)
	}
}

func TestSweepRemovesExpiredChallenges(t *testing.T) {
	f := newFixture(t)
	f.withTOTP(t)
	f.challenge(t)

	f.clock.Advance(auth.ChallengeTTL + time.Second)
	n, err := f.svc.SweepExpiredChallenges(context.Background())
	if err != nil || n != 1 {
		t.Errorf("SweepExpiredChallenges = %d, %v; want 1", n, err)
	}
}
