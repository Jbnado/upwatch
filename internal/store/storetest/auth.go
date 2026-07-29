package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// authCases são os casos de conformidade dos repositórios de autenticação.
func authCases() []conformanceCase {
	return []conformanceCase{
		{"UserCreateAssignsID", testUserCreateAssignsID},
		{"UserRoundTrips", testUserRoundTrips},
		{"UserGetMissingReturnsNotFound", testUserGetMissingReturnsNotFound},
		{"UserDuplicateUsernameReturnsConflict", testUserDuplicateUsernameReturnsConflict},
		{"UserGetByUsername", testUserGetByUsername},
		{"UserGetByUsernameMissingReturnsNotFound", testUserGetByUsernameMissingReturnsNotFound},
		{"UserUpdatePersistsChanges", testUserUpdatePersistsChanges},
		{"UserCountReflectsPopulation", testUserCountReflectsPopulation},

		{"SessionRoundTrips", testSessionRoundTrips},
		{"SessionGetMissingReturnsNotFound", testSessionGetMissingReturnsNotFound},
		{"SessionDelete", testSessionDelete},
		{"SessionDeleteExpiredKeepsValidOnes", testSessionDeleteExpiredKeepsValidOnes},
		{"SessionDeleteByUser", testSessionDeleteByUser},
		{"SessionCascadesOnUserDelete", testSessionCascadesOnUserDelete},

		{"TokenCreateAssignsID", testTokenCreateAssignsID},
		{"TokenGetByHash", testTokenGetByHash},
		{"TokenGetByHashMissingReturnsNotFound", testTokenGetByHashMissingReturnsNotFound},
		{"TokenListIsScopedToUser", testTokenListIsScopedToUser},
		{"TokenDelete", testTokenDelete},
		{"TokenTouchLastUsed", testTokenTouchLastUsed},
		{"TokenRoundTripsExpiry", testTokenRoundTripsExpiry},
	}
}

func mustCreateUser(t *testing.T, s store.Store, username string) domain.User {
	t.Helper()

	u := domain.User{Username: username}
	if err := u.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	if err := s.Users().Create(context.Background(), &u); err != nil {
		t.Fatalf("Create(%q) returned unexpected error: %v", username, err)
	}
	return u
}

// ---------- usuários ----------

func testUserCreateAssignsID(t *testing.T, newStore Factory) {
	s := newStore(t)

	u := mustCreateUser(t, s, "admin")

	if u.ID == 0 {
		t.Error("Create left ID as zero, want a generated identifier")
	}
	if u.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero after Create")
	}
}

// O hash precisa sobreviver à ida e volta intacto, senão ninguém consegue
// entrar depois de reiniciar o processo.
func testUserRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	created := mustCreateUser(t, s, "admin")

	got, err := s.Users().Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}

	if got.Username != "admin" {
		t.Errorf("Username = %q, want %q", got.Username, "admin")
	}
	if got.PasswordHash != created.PasswordHash {
		t.Error("PasswordHash did not survive the round trip")
	}
	if !got.CheckPassword("uma-senha-bem-longa") {
		t.Error("the stored hash no longer matches the original password")
	}
}

func testUserGetMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.Users().Get(context.Background(), 4242)

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get of a missing user returned %v, want store.ErrNotFound", err)
	}
}

func testUserDuplicateUsernameReturnsConflict(t *testing.T, newStore Factory) {
	s := newStore(t)
	mustCreateUser(t, s, "admin")

	dup := domain.User{Username: "admin", PasswordHash: "irrelevante"}
	err := s.Users().Create(context.Background(), &dup)

	if !errors.Is(err, store.ErrConflict) {
		t.Errorf("Create with a duplicate username returned %v, want store.ErrConflict", err)
	}
}

func testUserGetByUsername(t *testing.T, newStore Factory) {
	s := newStore(t)
	created := mustCreateUser(t, s, "admin")

	got, err := s.Users().GetByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetByUsername returned unexpected error: %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("GetByUsername returned user %d, want %d", got.ID, created.ID)
	}
}

func testUserGetByUsernameMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.Users().GetByUsername(context.Background(), "fantasma")

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetByUsername of a missing user returned %v, want store.ErrNotFound", err)
	}
}

func testUserUpdatePersistsChanges(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	if err := u.SetPassword("outra-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	if err := s.Users().Update(ctx, u); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	got, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if !got.CheckPassword("outra-senha-bem-longa") {
		t.Error("the new password does not work after Update")
	}
	if got.CheckPassword("uma-senha-bem-longa") {
		t.Error("the old password still works after Update")
	}
}

// A contagem decide se o assistente de primeiro acesso aparece; errar aqui
// deixaria a interface aberta ou inacessível.
func testUserCountReflectsPopulation(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	n, err := s.Users().Count(ctx)
	if err != nil {
		t.Fatalf("Count returned unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("Count on a fresh store = %d, want 0", n)
	}

	mustCreateUser(t, s, "admin")
	mustCreateUser(t, s, "operador")

	if n, err = s.Users().Count(ctx); err != nil {
		t.Fatalf("Count returned unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

// ---------- sessões ----------

func newSession(userID int64, hash string, expires time.Time) domain.Session {
	return domain.Session{
		Hash: hash, UserID: userID,
		CreatedAt: epoch, ExpiresAt: expires,
	}
}

func testSessionRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	want := newSession(u.ID, domain.HashToken("segredo-da-sessao"), epoch.Add(24*time.Hour))

	if err := s.Sessions().Create(ctx, want); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	got, err := s.Sessions().Get(ctx, want.Hash)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.UserID != u.ID {
		t.Errorf("UserID = %d, want %d", got.UserID, u.ID)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

func testSessionGetMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.Sessions().Get(context.Background(), "hash-inexistente")

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get of a missing session returned %v, want store.ErrNotFound", err)
	}
}

func testSessionDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	sess := newSession(u.ID, domain.HashToken("segredo"), epoch.Add(time.Hour))

	if err := s.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if err := s.Sessions().Delete(ctx, sess.Hash); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	if _, err := s.Sessions().Get(ctx, sess.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after Delete returned %v, want store.ErrNotFound", err)
	}
}

func testSessionDeleteExpiredKeepsValidOnes(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	expired := newSession(u.ID, domain.HashToken("velha"), epoch.Add(-time.Hour))
	valid := newSession(u.ID, domain.HashToken("nova"), epoch.Add(time.Hour))
	for _, sess := range []domain.Session{expired, valid} {
		if err := s.Sessions().Create(ctx, sess); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
	}

	removed, err := s.Sessions().DeleteExpired(ctx, epoch)
	if err != nil {
		t.Fatalf("DeleteExpired returned unexpected error: %v", err)
	}
	if removed != 1 {
		t.Errorf("DeleteExpired removed %d sessions, want 1", removed)
	}

	if _, err := s.Sessions().Get(ctx, valid.Hash); err != nil {
		t.Errorf("the valid session was removed: %v", err)
	}
}

// Trocar a senha precisa derrubar as sessões existentes; uma sessão que
// sobrevive à troca anula o motivo de trocá-la.
func testSessionDeleteByUser(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	target := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "operador")

	mine := newSession(target.ID, domain.HashToken("minha"), epoch.Add(time.Hour))
	theirs := newSession(other.ID, domain.HashToken("dele"), epoch.Add(time.Hour))
	for _, sess := range []domain.Session{mine, theirs} {
		if err := s.Sessions().Create(ctx, sess); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
	}

	if err := s.Sessions().DeleteByUser(ctx, target.ID); err != nil {
		t.Fatalf("DeleteByUser returned unexpected error: %v", err)
	}

	if _, err := s.Sessions().Get(ctx, mine.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the user's own session survived DeleteByUser: %v", err)
	}
	if _, err := s.Sessions().Get(ctx, theirs.Hash); err != nil {
		t.Errorf("another user's session was removed: %v", err)
	}
}

func testSessionCascadesOnUserDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	sess := newSession(u.ID, domain.HashToken("segredo"), epoch.Add(time.Hour))

	if err := s.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	if _, err := s.Sessions().DeleteExpired(ctx, epoch.Add(48*time.Hour)); err != nil {
		t.Fatalf("DeleteExpired returned unexpected error: %v", err)
	}

	if _, err := s.Sessions().Get(ctx, sess.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after the sweep returned %v, want store.ErrNotFound", err)
	}
}

// ---------- tokens ----------

func mustCreateToken(t *testing.T, s store.Store, userID int64, name string) (domain.APIToken, domain.Token) {
	t.Helper()

	secret, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}
	tok := domain.APIToken{
		UserID: userID, Name: name,
		Hash: secret.Hash, Prefix: secret.Prefix,
	}
	if err := s.Tokens().Create(context.Background(), &tok); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return tok, secret
}

func testTokenCreateAssignsID(t *testing.T, newStore Factory) {
	s := newStore(t)
	u := mustCreateUser(t, s, "admin")

	tok, _ := mustCreateToken(t, s, u.ID, "ci")

	if tok.ID == 0 {
		t.Error("Create left ID as zero, want a generated identifier")
	}
	if tok.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero after Create")
	}
}

// A busca pelo hash é o caminho de toda requisição autenticada por token.
func testTokenGetByHash(t *testing.T, newStore Factory) {
	s := newStore(t)
	u := mustCreateUser(t, s, "admin")
	created, secret := mustCreateToken(t, s, u.ID, "ci")

	got, err := s.Tokens().GetByHash(context.Background(), domain.HashToken(secret.Secret))
	if err != nil {
		t.Fatalf("GetByHash returned unexpected error: %v", err)
	}

	if got.ID != created.ID {
		t.Errorf("GetByHash returned token %d, want %d", got.ID, created.ID)
	}
	if got.UserID != u.ID {
		t.Errorf("UserID = %d, want %d", got.UserID, u.ID)
	}
	if got.Prefix != created.Prefix {
		t.Errorf("Prefix = %q, want %q", got.Prefix, created.Prefix)
	}
}

func testTokenGetByHashMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.Tokens().GetByHash(context.Background(), domain.HashToken("nunca-existiu"))

	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetByHash of a missing token returned %v, want store.ErrNotFound", err)
	}
}

func testTokenListIsScopedToUser(t *testing.T, newStore Factory) {
	s := newStore(t)
	mine := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "operador")
	mustCreateToken(t, s, mine.ID, "ci")
	mustCreateToken(t, s, mine.ID, "deploy")
	mustCreateToken(t, s, other.ID, "alheio")

	got, err := s.Tokens().List(context.Background(), mine.ID)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("List returned %d tokens, want 2", len(got))
	}
	for _, tok := range got {
		if tok.UserID != mine.ID {
			t.Errorf("List returned a token belonging to user %d", tok.UserID)
		}
	}
}

func testTokenDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	tok, secret := mustCreateToken(t, s, u.ID, "ci")

	if err := s.Tokens().Delete(ctx, tok.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	_, err := s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret))
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the revoked token still authenticates: %v", err)
	}
}

// Saber quando o token foi usado pela última vez é o que permite revogar
// credenciais esquecidas com segurança.
func testTokenTouchLastUsed(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	tok, secret := mustCreateToken(t, s, u.ID, "ci")

	if tok.LastUsedAt != nil {
		t.Error("LastUsedAt is set on a token that was never used")
	}

	used := epoch.Add(time.Hour)
	if err := s.Tokens().TouchLastUsed(ctx, tok.ID, used); err != nil {
		t.Fatalf("TouchLastUsed returned unexpected error: %v", err)
	}

	got, err := s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret))
	if err != nil {
		t.Fatalf("GetByHash returned unexpected error: %v", err)
	}
	if got.LastUsedAt == nil {
		t.Fatal("LastUsedAt is still nil after TouchLastUsed")
	}
	if !got.LastUsedAt.Equal(used) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, used)
	}
}

func testTokenRoundTripsExpiry(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")

	secret, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}
	expires := epoch.Add(30 * 24 * time.Hour)
	tok := domain.APIToken{
		UserID: u.ID, Name: "temporário",
		Hash: secret.Hash, Prefix: secret.Prefix, ExpiresAt: &expires,
	}
	if err := s.Tokens().Create(ctx, &tok); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	got, err := s.Tokens().GetByHash(ctx, secret.Hash)
	if err != nil {
		t.Fatalf("GetByHash returned unexpected error: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Fatal("ExpiresAt is nil after the round trip")
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
}
