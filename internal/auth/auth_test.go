package auth_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

const goodPassword = "uma-senha-bem-longa"

type fixture struct {
	store *sqlstore.Store
	clock *clock.Fake
	svc   *auth.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := clock.NewFake(epoch)
	return &fixture{
		store: st,
		clock: fake,
		svc:   auth.New(st, auth.Options{Clock: fake}),
	}
}

// withAdmin cria a conta inicial e devolve o usuário.
func (f *fixture) withAdmin(t *testing.T) domain.User {
	t.Helper()

	u, err := f.svc.CreateInitialAdmin(context.Background(), "admin", goodPassword)
	if err != nil {
		t.Fatalf("CreateInitialAdmin returned unexpected error: %v", err)
	}
	return u
}

// ---------- primeiro acesso ----------

func TestNeedsSetupOnFreshInstall(t *testing.T) {
	f := newFixture(t)

	need, err := f.svc.NeedsSetup(context.Background())
	if err != nil {
		t.Fatalf("NeedsSetup returned unexpected error: %v", err)
	}
	if !need {
		t.Error("NeedsSetup() = false on a fresh install, want true")
	}
}

func TestNeedsSetupIsFalseOnceAdminExists(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	need, err := f.svc.NeedsSetup(context.Background())
	if err != nil {
		t.Fatalf("NeedsSetup returned unexpected error: %v", err)
	}
	if need {
		t.Error("NeedsSetup() = true after the admin was created, want false")
	}
}

// O assistente de primeiro acesso não é autenticado; se continuasse
// disponível depois do primeiro uso, qualquer visitante criaria uma conta
// e tomaria a instalação.
func TestCreateInitialAdminRefusesSecondCall(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	_, err := f.svc.CreateInitialAdmin(context.Background(), "intruso", goodPassword)

	if !errors.Is(err, auth.ErrSetupComplete) {
		t.Errorf("second CreateInitialAdmin returned %v, want auth.ErrSetupComplete", err)
	}
}

func TestCreateInitialAdminRejectsWeakPassword(t *testing.T) {
	f := newFixture(t)

	_, err := f.svc.CreateInitialAdmin(context.Background(), "admin", "curta")

	if err == nil {
		t.Fatal("CreateInitialAdmin with a weak password returned nil error, want an error")
	}
}

// ---------- login ----------

func TestLoginWithCorrectPasswordIssuesSession(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	secret, err := f.svc.Login(context.Background(), "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if secret == "" {
		t.Fatal("Login returned an empty session secret")
	}

	// O que fica no banco é o hash; o segredo cru só existe no cookie.
	if _, err := f.store.Sessions().Get(context.Background(), domain.HashToken(secret)); err != nil {
		t.Errorf("the issued session was not persisted: %v", err)
	}
	if _, err := f.store.Sessions().Get(context.Background(), secret); err == nil {
		t.Error("the raw secret is stored as-is, want only its hash")
	}
}

func TestLoginWithWrongPasswordFails(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	_, err := f.svc.Login(context.Background(), "admin", "senha-errada-porem-longa")

	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("Login returned %v, want auth.ErrInvalidCredentials", err)
	}
}

// Usuário inexistente e senha errada precisam devolver exatamente o mesmo
// erro. Diferenciá-los permitiria descobrir quais contas existem antes de
// atacar a senha.
func TestLoginDoesNotRevealWhetherUserExists(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	_, wrongPassword := f.svc.Login(ctx, "admin", "senha-errada-porem-longa")
	_, unknownUser := f.svc.Login(ctx, "nao-existe", "senha-errada-porem-longa")

	if wrongPassword == nil || unknownUser == nil {
		t.Fatal("both attempts should have failed")
	}
	if wrongPassword.Error() != unknownUser.Error() {
		t.Errorf("wrong password says %q but unknown user says %q; the two must be indistinguishable",
			wrongPassword, unknownUser)
	}
	if !errors.Is(unknownUser, auth.ErrInvalidCredentials) {
		t.Errorf("unknown user returned %v, want auth.ErrInvalidCredentials", unknownUser)
	}
}

// Sem limite, uma interface exposta à internet vira alvo de força bruta
// contínua.
func TestLoginIsRateLimitedAfterRepeatedFailures(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	for i := 0; i < auth.MaxLoginAttempts; i++ {
		if _, err := f.svc.Login(ctx, "admin", "senha-errada-porem-longa"); err == nil {
			t.Fatal("a wrong password succeeded")
		}
	}

	// Agora nem a senha correta passa, enquanto a janela durar.
	_, err := f.svc.Login(ctx, "admin", goodPassword)
	if !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Errorf("Login after %d failures returned %v, want auth.ErrTooManyAttempts",
			auth.MaxLoginAttempts, err)
	}
}

func TestLoginRecoversAfterLockoutWindow(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	for i := 0; i < auth.MaxLoginAttempts; i++ {
		f.svc.Login(ctx, "admin", "senha-errada-porem-longa") //nolint:errcheck // falha esperada
	}

	// Confirma que o bloqueio está de fato em vigor antes de avançar o
	// relógio; sem esta checagem o teste passaria mesmo com o limitador
	// desligado.
	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Fatalf("the account is not locked out; got %v", err)
	}

	f.clock.Advance(auth.LockoutWindow + time.Second)

	if _, err := f.svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Errorf("Login after the lockout window returned %v, want success", err)
	}
}

// Acertar a senha limpa o histórico: caso contrário, alguém que errou
// algumas vezes ficaria a poucas tentativas de ser bloqueado para sempre.
func TestSuccessfulLoginClearsFailureCount(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	for i := 0; i < auth.MaxLoginAttempts-1; i++ {
		f.svc.Login(ctx, "admin", "senha-errada-porem-longa") //nolint:errcheck // falha esperada
	}
	if _, err := f.svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Fatalf("Login with the correct password returned %v", err)
	}

	for i := 0; i < auth.MaxLoginAttempts-1; i++ {
		if _, err := f.svc.Login(ctx, "admin", "senha-errada-porem-longa"); errors.Is(err, auth.ErrTooManyAttempts) {
			t.Fatal("the failure count was not reset by the successful login")
		}
	}
}

// O bloqueio é por conta: travar uma não pode impedir o acesso às demais.
func TestLockoutIsScopedToTheUsername(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	other := domain.User{Username: "operador"}
	if err := other.SetPassword(goodPassword); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	if err := f.store.Users().Create(ctx, &other); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	for i := 0; i < auth.MaxLoginAttempts; i++ {
		f.svc.Login(ctx, "admin", "senha-errada-porem-longa") //nolint:errcheck // falha esperada
	}

	if _, err := f.svc.Login(ctx, "operador", goodPassword); err != nil {
		t.Errorf("locking out one account blocked another: %v", err)
	}
}

// ---------- sessões ----------

func TestAuthenticateSessionReturnsTheUser(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	secret, err := f.svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}

	got, err := f.svc.AuthenticateSession(ctx, secret)
	if err != nil {
		t.Fatalf("AuthenticateSession returned unexpected error: %v", err)
	}
	if got.ID != admin.ID {
		t.Errorf("authenticated user %d, want %d", got.ID, admin.ID)
	}
}

func TestAuthenticateSessionRejectsUnknownSecret(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	_, err := f.svc.AuthenticateSession(context.Background(), "nunca-existiu")

	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("AuthenticateSession returned %v, want auth.ErrUnauthenticated", err)
	}
}

func TestAuthenticateSessionRejectsExpiredSession(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	secret, err := f.svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	f.clock.Advance(auth.DefaultSessionTTL + time.Minute)

	if _, err := f.svc.AuthenticateSession(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("AuthenticateSession on an expired session returned %v, want auth.ErrUnauthenticated", err)
	}
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	secret, err := f.svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if err := f.svc.Logout(ctx, secret); err != nil {
		t.Fatalf("Logout returned unexpected error: %v", err)
	}

	if _, err := f.svc.AuthenticateSession(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("the session still authenticates after Logout: %v", err)
	}
}

// ---------- troca de senha ----------

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)

	err := f.svc.ChangePassword(context.Background(), admin.ID, "senha-errada-porem-longa", "nova-senha-bem-longa")

	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("ChangePassword returned %v, want auth.ErrInvalidCredentials", err)
	}
}

func TestChangePasswordReplacesTheCredential(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	if err := f.svc.ChangePassword(ctx, admin.ID, goodPassword, "nova-senha-bem-longa"); err != nil {
		t.Fatalf("ChangePassword returned unexpected error: %v", err)
	}

	if _, err := f.svc.Login(ctx, "admin", "nova-senha-bem-longa"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := f.svc.Login(ctx, "admin", goodPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Error("the old password still works after the change")
	}
}

// Trocar a senha porque ela pode ter vazado só faz sentido se derrubar as
// sessões abertas; do contrário quem a obteve continua dentro.
func TestChangePasswordEndsExistingSessions(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	secret, err := f.svc.Login(ctx, "admin", goodPassword)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if err := f.svc.ChangePassword(ctx, admin.ID, goodPassword, "nova-senha-bem-longa"); err != nil {
		t.Fatalf("ChangePassword returned unexpected error: %v", err)
	}

	if _, err := f.svc.AuthenticateSession(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Error("a session opened before the password change still authenticates")
	}
}

func TestChangePasswordRejectsWeakReplacement(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)

	err := f.svc.ChangePassword(context.Background(), admin.ID, goodPassword, "curta")

	if err == nil {
		t.Fatal("ChangePassword to a weak password returned nil error, want an error")
	}
}

// ---------- tokens ----------

func TestIssueTokenReturnsTheSecretOnlyOnce(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	tok, secret, err := f.svc.IssueToken(ctx, admin.ID, "ci", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	if secret == "" {
		t.Fatal("IssueToken returned an empty secret")
	}

	listed, err := f.svc.ListTokens(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ListTokens returned unexpected error: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListTokens returned %d tokens, want 1", len(listed))
	}
	if listed[0].Hash == secret {
		t.Error("the listing exposes the raw secret, want only its hash internally")
	}
	if listed[0].Prefix != tok.Prefix {
		t.Errorf("Prefix = %q, want %q", listed[0].Prefix, tok.Prefix)
	}
}

func TestAuthenticateTokenReturnsTheOwner(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	_, secret, err := f.svc.IssueToken(ctx, admin.ID, "ci", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}

	got, err := f.svc.AuthenticateToken(ctx, secret)
	if err != nil {
		t.Fatalf("AuthenticateToken returned unexpected error: %v", err)
	}
	if got.ID != admin.ID {
		t.Errorf("authenticated user %d, want %d", got.ID, admin.ID)
	}
}

func TestAuthenticateTokenRejectsUnknownSecret(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)

	_, err := f.svc.AuthenticateToken(context.Background(), "upw_inventado")

	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("AuthenticateToken returned %v, want auth.ErrUnauthenticated", err)
	}
}

func TestAuthenticateTokenRejectsExpiredToken(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	expires := epoch.Add(time.Hour)
	_, secret, err := f.svc.IssueToken(ctx, admin.ID, "temporário", &expires)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	f.clock.Advance(2 * time.Hour)

	if _, err := f.svc.AuthenticateToken(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("an expired token still authenticates: %v", err)
	}
}

func TestAuthenticateTokenRecordsUsage(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	_, secret, err := f.svc.IssueToken(ctx, admin.ID, "ci", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	if _, err := f.svc.AuthenticateToken(ctx, secret); err != nil {
		t.Fatalf("AuthenticateToken returned unexpected error: %v", err)
	}

	listed, err := f.svc.ListTokens(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ListTokens returned unexpected error: %v", err)
	}
	if listed[0].LastUsedAt == nil {
		t.Error("LastUsedAt is nil after the token authenticated a request")
	}
}

func TestRevokeTokenStopsAuthentication(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	tok, secret, err := f.svc.IssueToken(ctx, admin.ID, "ci", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
	if err := f.svc.RevokeToken(ctx, admin.ID, tok.ID); err != nil {
		t.Fatalf("RevokeToken returned unexpected error: %v", err)
	}

	if _, err := f.svc.AuthenticateToken(ctx, secret); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Error("the revoked token still authenticates")
	}
}

// Revogar token alheio seria escalonamento de privilégio assim que houver
// mais de uma conta.
func TestRevokeTokenRefusesAnotherUsersToken(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)
	ctx := context.Background()

	other := domain.User{Username: "operador"}
	if err := other.SetPassword(goodPassword); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	if err := f.store.Users().Create(ctx, &other); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	tok, _, err := f.svc.IssueToken(ctx, other.ID, "alheio", nil)
	if err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}

	if err := f.svc.RevokeToken(ctx, admin.ID, tok.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RevokeToken on another user's token returned %v, want store.ErrNotFound", err)
	}
}

func TestIssueTokenRequiresName(t *testing.T) {
	f := newFixture(t)
	admin := f.withAdmin(t)

	_, _, err := f.svc.IssueToken(context.Background(), admin.ID, "   ", nil)

	if err == nil {
		t.Fatal("IssueToken without a name returned nil error, want an error")
	}
}

// Sessões vencidas acumulariam indefinidamente numa instalação de longa
// duração.
func TestSweepRemovesExpiredSessions(t *testing.T) {
	f := newFixture(t)
	f.withAdmin(t)
	ctx := context.Background()

	if _, err := f.svc.Login(ctx, "admin", goodPassword); err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	f.clock.Advance(auth.DefaultSessionTTL + time.Hour)

	removed, err := f.svc.SweepExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredSessions returned unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("SweepExpiredSessions removed %d sessions, want 1", removed)
	}
}
