package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
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
		{"UserSetPasswordHashTouchesOnlyThePassword", testUserSetPasswordHashTouchesOnlyThePassword},
		{"UserSetPasswordHashIfKeepsConcurrentPassword", testUserSetPasswordHashIfKeepsConcurrentPassword},
		{"UserSetPasswordHashIfTouchesOnlyHash", testUserSetPasswordHashIfTouchesOnlyHash},
		{"UserSetAccessKeepsThePassword", testUserSetAccessKeepsThePassword},
		{"UserSetAccessAndDeleteKeepAFullAdmin", testUserSetAccessAndDeleteKeepAFullAdmin},
		{"UserConcurrentDemotionsKeepAFullAdmin", testUserConcurrentDemotionsKeepAFullAdmin},
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
		{"TokenRevokeRetainsRecord", testTokenRevokeRetainsRecord},
		{"TokenRevokeAllByUser", testTokenRevokeAllByUser},
		{"TokenRevokeOthers", testTokenRevokeOthers},
		{"TokenTouchLastUsed", testTokenTouchLastUsed},
		{"TokenRoundTripsExpiry", testTokenRoundTripsExpiry},
		{"TokenSetExpiry", testTokenSetExpiry},
		{"TokenListExpiring", testTokenListExpiring},
		{"TokenMarkExpiryWarned", testTokenMarkExpiryWarned},
		{"TokenSetExpiryClearsWarning", testTokenSetExpiryClearsWarning},
		{"TokenRotateKeepsBothValid", testTokenRotateKeepsBothValid},
		{"TokenSuccessorUsed", testTokenSuccessorUsed},
		{"TokenCascadesOnUserDelete", testTokenCascadesOnUserDelete},
	}
}

func testUserSetPasswordHashIfKeepsConcurrentPassword(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "bia")
	next := u
	if err := next.SetPassword("outra-senha-bem-longa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().SetPasswordHash(ctx, u.ID, next.PasswordHash); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().SetPasswordHashIf(ctx, u.ID, u.PasswordHash, "hash-obsoleto"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("hash esperado vencido retornou %v, esperado conflito", err)
	}
	got, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CheckPassword("outra-senha-bem-longa") || got.CheckPassword("uma-senha-bem-longa") {
		t.Error("a regravação sobrescreveu a troca de senha concorrente")
	}
	if err := s.Users().SetPasswordHashIf(ctx, 9999, u.PasswordHash, "novo-hash"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("conta ausente retornou %v, esperado conflito", err)
	}
}

func testUserSetPasswordHashIfTouchesOnlyHash(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	mustCreateUser(t, s, "admin")
	g := mustCreateGroup(t, s, "RPA", nil)
	u := mustCreateUser(t, s, "bia")
	scope := domain.Scope{Restricted: true, GroupIDs: []int64{g.ID}}
	if err := s.Users().SetAccess(ctx, u.ID, domain.RoleViewer, &scope); err != nil {
		t.Fatal(err)
	}
	before, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A precisão dos dois bancos é de milissegundos: uma escrita indevida
	// no carimbo precisa ocorrer num instante diferente para ser detectada.
	time.Sleep(5 * time.Millisecond)
	if err := s.Users().SetPasswordHashIf(ctx, u.ID, u.PasswordHash, "hash-regravado"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "hash-regravado" {
		t.Errorf("hash não foi regravado: %q", got.PasswordHash)
	}
	got.PasswordHash = before.PasswordHash
	if !reflect.DeepEqual(got, before) {
		t.Errorf("regravação alterou outros campos: antes %+v; depois %+v", before, got)
	}
}

func mustCreateUser(t *testing.T, s store.Store, username string) domain.User {
	t.Helper()

	u := domain.User{Username: username, Role: domain.RoleAdmin}
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

// Trocar a senha grava a senha e nada mais. Regravar a conta inteira
// devolvia papel e escopo lidos antes da troca, e uma revogação feita no
// meio sumia sem ninguém ver.
func testUserSetPasswordHashTouchesOnlyThePassword(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	mustCreateUser(t, s, "admin")
	g := mustCreateGroup(t, s, "RPA", nil)

	u := mustCreateUser(t, s, "bia")
	restrito := domain.Scope{Restricted: true, GroupIDs: []int64{g.ID}}
	if err := s.Users().SetAccess(ctx, u.ID, domain.RoleViewer, &restrito); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}

	novo := u
	if err := novo.SetPassword("outra-senha-bem-longa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().SetPasswordHash(ctx, u.ID, novo.PasswordHash); err != nil {
		t.Fatalf("SetPasswordHash returned unexpected error: %v", err)
	}

	got, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if !got.CheckPassword("outra-senha-bem-longa") || got.CheckPassword("uma-senha-bem-longa") {
		t.Error("the password was not replaced")
	}
	if got.Role != domain.RoleViewer || !got.Scope.Restricted || len(got.Scope.GroupIDs) != 1 {
		t.Errorf("SetPasswordHash changed the access: role %v, scope %+v", got.Role, got.Scope)
	}

	if err := s.Users().SetPasswordHash(ctx, 9999, novo.PasswordHash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetPasswordHash on a missing account returned %v, want ErrNotFound", err)
	}
}

// Trocar papel e escopo grava papel e escopo e nada mais: regravar a
// senha lida antes ressuscitaria a antiga, justamente a que a pessoa quis
// abandonar.
func testUserSetAccessKeepsThePassword(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	mustCreateUser(t, s, "admin")
	a := mustCreateGroup(t, s, "A", nil)
	b := mustCreateGroup(t, s, "B", nil)
	u := mustCreateUser(t, s, "bia")

	novo := u
	if err := novo.SetPassword("outra-senha-bem-longa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().SetPasswordHash(ctx, u.ID, novo.PasswordHash); err != nil {
		t.Fatal(err)
	}

	escopo := domain.Scope{Restricted: true, GroupIDs: []int64{b.ID, a.ID}}
	if err := s.Users().SetAccess(ctx, u.ID, domain.RoleViewer, &escopo); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}
	got, _ := s.Users().Get(ctx, u.ID)
	if !got.CheckPassword("outra-senha-bem-longa") {
		t.Error("SetAccess changed the password")
	}
	if got.Role != domain.RoleViewer || len(got.Scope.GroupIDs) != 2 {
		t.Errorf("after SetAccess: role %v, scope %+v", got.Role, got.Scope)
	}

	// Escopo nulo mantém o atual: trocar só o papel não pode alargar o
	// alcance por omissão de um campo.
	if err := s.Users().SetAccess(ctx, u.ID, domain.RoleAdmin, nil); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}
	got, _ = s.Users().Get(ctx, u.ID)
	if got.Role != domain.RoleAdmin || !got.Scope.Restricted || len(got.Scope.GroupIDs) != 2 {
		t.Errorf("after a role-only change: role %v, scope %+v", got.Role, got.Scope)
	}

	if err := s.Users().SetAccess(ctx, 9999, domain.RoleViewer, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetAccess on a missing account returned %v, want ErrNotFound", err)
	}
}

// Rebaixar, restringir ou apagar o último administrador de escopo total é
// recusado pelo próprio store, na mesma transação da escrita. Conferir
// antes, fora dela, deixava dois pedidos simultâneos verem "sobra o
// outro" e gravarem os dois.
func testUserSetAccessAndDeleteKeepAFullAdmin(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	g := mustCreateGroup(t, s, "RPA", nil)
	admin := mustCreateUser(t, s, "admin")
	restrito := domain.Scope{Restricted: true, GroupIDs: []int64{g.ID}}

	for nome, err := range map[string]error{
		"rebaixar":   s.Users().SetAccess(ctx, admin.ID, domain.RoleViewer, nil),
		"restringir": s.Users().SetAccess(ctx, admin.ID, domain.RoleAdmin, &restrito),
		"apagar":     s.Users().Delete(ctx, admin.ID),
	} {
		if !errors.Is(err, store.ErrLastFullAdmin) {
			t.Errorf("%s o último administrador: err = %v, want ErrLastFullAdmin", nome, err)
		}
	}
	got, err := s.Users().Get(ctx, admin.ID)
	if err != nil || got.Role != domain.RoleAdmin || got.Scope.Restricted {
		t.Fatalf("a recusa deixou a conta alterada: %+v, %v", got, err)
	}

	// Com outro administrador de escopo total, pode.
	mustCreateUser(t, s, "outro")
	if err := s.Users().SetAccess(ctx, admin.ID, domain.RoleAdmin, &restrito); err != nil {
		t.Fatalf("restringir com outro administrador ao lado: %v", err)
	}
	// Conta que não administra tudo sai à vontade.
	if err := s.Users().Delete(ctx, admin.ID); err != nil {
		t.Fatalf("apagar administrador restrito: %v", err)
	}
}

// Vários administradores de escopo total saem ao mesmo tempo, uns
// rebaixados e outros apagados. Sobra exatamente um.
func testUserConcurrentDemotionsKeepAFullAdmin(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	const n = 6
	var ids []int64
	for i := 0; i < n; i++ {
		ids = append(ids, mustCreateUser(t, s, fmt.Sprintf("admin-%d", i)).ID)
	}

	var wg sync.WaitGroup
	erros := make([]error, n)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				erros[i] = s.Users().SetAccess(ctx, id, domain.RoleViewer, nil)
			} else {
				erros[i] = s.Users().Delete(ctx, id)
			}
		}()
	}
	wg.Wait()

	recusas := 0
	for _, err := range erros {
		switch {
		case err == nil:
		case errors.Is(err, store.ErrLastFullAdmin):
			recusas++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	contas, err := s.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	totais := 0
	for _, c := range contas {
		if c.Role == domain.RoleAdmin && !c.Scope.Restricted {
			totais++
		}
	}
	if totais != 1 || recusas != 1 {
		t.Errorf("sobraram %d administradores de escopo total e houve %d recusas; want 1 e 1", totais, recusas)
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

// Revogar retém o registro: o token para de autenticar e some da listagem
// ativa, mas a linha fica, com quando, por que e por quem — é o que a
// auditoria lê depois. Revogar de novo não sobrescreve o ator original.
func testTokenRevokeRetainsRecord(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	ator := mustCreateUser(t, s, "admin")
	u := mustCreateUser(t, s, "bia")
	tok, secret := mustCreateToken(t, s, u.ID, "ci")

	if err := s.Tokens().Revoke(ctx, tok.ID, domain.TokenRevokeManual, &ator.ID); err != nil {
		t.Fatalf("Revoke returned unexpected error: %v", err)
	}

	// Não autentica mais.
	if _, err := s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the revoked token still authenticates: %v", err)
	}
	// Some da listagem ativa.
	if n := len(mustListTokens(t, s, u.ID)); n != 0 {
		t.Errorf("o token revogado ainda aparece na listagem ativa: %d", n)
	}
	// Mas o registro fica, com motivo e ator.
	got, err := s.Tokens().Get(ctx, tok.ID)
	if err != nil {
		t.Fatalf("o registro do token revogado não foi retido: %v", err)
	}
	if got.RevokedAt == nil || got.RevokedReason != domain.TokenRevokeManual ||
		got.RevokedBy == nil || *got.RevokedBy != ator.ID {
		t.Errorf("registro de revogação incompleto: %+v", got)
	}

	// Revogar de novo é ErrNotFound: o token já não está ativo, e o ator e o
	// motivo originais não podem ser sobrescritos por uma segunda chamada.
	if err := s.Tokens().Revoke(ctx, tok.ID, domain.TokenRevokeReuse, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("revogar de novo = %v, want ErrNotFound", err)
	}

	// Get de token inexistente continua ErrNotFound.
	if _, err := s.Tokens().Get(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get de token inexistente = %v, want ErrNotFound", err)
	}
}

// Revogar em massa é o que a troca de senha e as ações de segundo fator
// fazem com as sessões, e passaram a fazer com os tokens: a credencial que
// um invasor emitiu não pode atravessar a resposta ao incidente. E tem de
// alcançar só a conta pedida, senão responder a um incidente derruba a
// automação da instalação inteira.
//
// Confere por GetByHash, e não contando o que List devolve: o contrato é
// "o token deixa de autenticar", e é GetByHash que autentica (ver
// testTokenDelete). Um backend que marcasse o token de um jeito que List
// respeita e GetByHash não passaria a contagem com a credencial viva.
func testTokenRevokeAllByUser(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	target := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "operador")
	_, meu := mustCreateToken(t, s, target.ID, "ci")
	_, outroMeu := mustCreateToken(t, s, target.ID, "backup")
	_, dele := mustCreateToken(t, s, other.ID, "dele")

	// Devolve a quantidade: é o que a auditoria da revogação em massa
	// registra.
	n, err := s.Tokens().RevokeAllByUser(ctx, target.ID, domain.TokenRevokeManual, &other.ID)
	if err != nil {
		t.Fatalf("RevokeAllByUser returned unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("RevokeAllByUser revogou %d, want 2", n)
	}

	for _, tok := range []domain.Token{meu, outroMeu} {
		if _, err := s.Tokens().GetByHash(ctx, tok.Hash); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("token revogado ainda autentica: %v", err)
		}
	}
	if _, err := s.Tokens().GetByHash(ctx, dele.Hash); err != nil {
		t.Errorf("token de outra conta deixou de autenticar: %v", err)
	}

	// Conta sem token ativo devolve zero, não erro: a troca de senha de quem
	// nunca emitiu token chama isto do mesmo jeito.
	if n, err := s.Tokens().RevokeAllByUser(ctx, target.ID, domain.TokenRevokeManual, &other.ID); err != nil || n != 0 {
		t.Errorf("RevokeAllByUser numa conta já revogada = %d, %v; want 0, nil", n, err)
	}
}

// DeleteOthers preserva a credencial que está fazendo a mudança, que é o
// que liga ou desliga o segundo fator por token de API. Sem isso aquela
// chamada devolve sucesso e mata o próprio chamador.
func testTokenRevokeOthers(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	target := mustCreateUser(t, s, "admin")
	other := mustCreateUser(t, s, "operador")
	_, chamador := mustCreateToken(t, s, target.ID, "ci")
	_, antigo := mustCreateToken(t, s, target.ID, "automacao-antiga")
	_, dele := mustCreateToken(t, s, other.ID, "dele")

	if err := s.Tokens().RevokeOthers(ctx, target.ID, chamador.Hash, domain.TokenRevokeManual, &target.ID); err != nil {
		t.Fatalf("RevokeOthers returned unexpected error: %v", err)
	}

	if _, err := s.Tokens().GetByHash(ctx, chamador.Hash); err != nil {
		t.Errorf("o token que pediu a mudança deixou de autenticar: %v", err)
	}
	if _, err := s.Tokens().GetByHash(ctx, antigo.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("token revogado ainda autentica: %v", err)
	}
	if _, err := s.Tokens().GetByHash(ctx, dele.Hash); err != nil {
		t.Errorf("token de outra conta deixou de autenticar: %v", err)
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

// Editar a expiração grava só a coluna de expiração: o dono pode pôr prazo
// num token que nasceu sem, encurtar, esticar ou removê-lo de novo, e o
// resto do registro — nome, prefixo, criação — fica como estava.
func testTokenSetExpiry(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	tok, secret := mustCreateToken(t, s, u.ID, "ci") // nasce sem expiração

	prazo := epoch.Add(30 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, tok.ID, &prazo); err != nil {
		t.Fatalf("SetExpiry returned unexpected error: %v", err)
	}
	got, err := s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(prazo) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, prazo)
	}
	// Só a expiração mudou.
	if got.Name != tok.Name || got.Prefix != tok.Prefix || !got.CreatedAt.Equal(tok.CreatedAt) {
		t.Errorf("SetExpiry alterou outros campos: %+v (criado %+v)", got, tok)
	}

	// Remover o prazo volta a "sem expiração".
	if err := s.Tokens().SetExpiry(ctx, tok.ID, nil); err != nil {
		t.Fatalf("SetExpiry(nil) returned unexpected error: %v", err)
	}
	got, err = s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v após remover, want nil", got.ExpiresAt)
	}

	// Token inexistente é ErrNotFound: a interface pede por id, e um id que
	// não é de ninguém não pode passar calado.
	if err := s.Tokens().SetExpiry(ctx, 9999, &prazo); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetExpiry em token inexistente = %v, want ErrNotFound", err)
	}
}

// ListExpiring devolve os tokens ativos cuja expiração se aproxima, com o
// dono e o marco já avisado, e deixa de fora os revogados e os sem prazo. É
// a leitura do verificador de avisos de expiração.
func testTokenListExpiring(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	dono := mustCreateUser(t, s, "dona")

	// Vence dentro da janela: é o único que deve voltar.
	venceLogo, _ := mustCreateToken(t, s, dono.ID, "deploy")
	prazoLogo := epoch.Add(5 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, venceLogo.ID, &prazoLogo); err != nil {
		t.Fatal(err)
	}
	// Vence muito depois da janela.
	venceTarde, _ := mustCreateToken(t, s, dono.ID, "longe")
	prazoTarde := epoch.Add(90 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, venceTarde.ID, &prazoTarde); err != nil {
		t.Fatal(err)
	}
	// Sem prazo: nunca vence, nunca entra.
	mustCreateToken(t, s, dono.ID, "eterno")
	// Revogado, ainda que venha a vencer: a credencial já está morta.
	revogado, _ := mustCreateToken(t, s, dono.ID, "revogado")
	if err := s.Tokens().SetExpiry(ctx, revogado.ID, &prazoLogo); err != nil {
		t.Fatal(err)
	}
	if err := s.Tokens().Revoke(ctx, revogado.ID, domain.TokenRevokeManual, nil); err != nil {
		t.Fatal(err)
	}

	janela := epoch.Add(30 * 24 * time.Hour)
	got, err := s.Tokens().ListExpiring(ctx, janela)
	if err != nil {
		t.Fatalf("ListExpiring returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListExpiring devolveu %d tokens, want 1 (ativo, com prazo, dentro da janela)", len(got))
	}
	et := got[0]
	if et.TokenID != venceLogo.ID {
		t.Errorf("ListExpiring devolveu o token %d, want %d", et.TokenID, venceLogo.ID)
	}
	if et.UserID != dono.ID {
		t.Errorf("UserID = %d, want %d", et.UserID, dono.ID)
	}
	if et.Name != venceLogo.Name || et.Prefix != venceLogo.Prefix {
		t.Errorf("identificação = %q/%q, want %q/%q", et.Name, et.Prefix, venceLogo.Name, venceLogo.Prefix)
	}
	if et.Username != dono.Username {
		t.Errorf("Username = %q, want %q", et.Username, dono.Username)
	}
	if !et.ExpiresAt.Equal(prazoLogo) {
		t.Errorf("ExpiresAt = %v, want %v", et.ExpiresAt, prazoLogo)
	}
	if et.WarnedDays != nil {
		t.Errorf("WarnedDays = %v num token nunca avisado, want nil", et.WarnedDays)
	}
}

// MarkExpiryWarned grava o marco mais fino já avisado e só avança rumo ao
// vencimento: avisar em 7 depois de 30 grava 7, mas "avisar em 30" de novo,
// ou voltar a um marco mais distante, não grava. Devolve se gravou, para
// dois ticks não repetirem o mesmo aviso.
func testTokenMarkExpiryWarned(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	dono := mustCreateUser(t, s, "dona")
	tok, _ := mustCreateToken(t, s, dono.ID, "deploy")
	prazo := epoch.Add(5 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, tok.ID, &prazo); err != nil {
		t.Fatal(err)
	}

	warned := func() *int {
		t.Helper()
		got, err := s.Tokens().ListExpiring(ctx, epoch.Add(30*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, et := range got {
			if et.TokenID == tok.ID {
				return et.WarnedDays
			}
		}
		t.Fatalf("token %d sumiu da listagem de expiração", tok.ID)
		return nil
	}

	// Primeiro aviso, no marco de 30, grava.
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, tok.ID, 30); err != nil || !ok {
		t.Fatalf("MarkExpiryWarned(30) = %v, %v; want true, nil", ok, err)
	}
	if w := warned(); w == nil || *w != 30 {
		t.Fatalf("marco gravado = %v, want 30", w)
	}

	// Marcar 30 de novo não grava: já avisado nesse marco.
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, tok.ID, 30); err != nil || ok {
		t.Fatalf("MarkExpiryWarned(30) repetido = %v, %v; want false, nil", ok, err)
	}

	// Avançar para 7 (mais perto do vencimento) grava.
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, tok.ID, 7); err != nil || !ok {
		t.Fatalf("MarkExpiryWarned(7) = %v, %v; want true, nil", ok, err)
	}
	if w := warned(); w == nil || *w != 7 {
		t.Fatalf("marco gravado = %v, want 7", w)
	}

	// Voltar a um marco mais distante (30) não grava nem regride.
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, tok.ID, 30); err != nil || ok {
		t.Fatalf("MarkExpiryWarned(30) após 7 = %v, %v; want false, nil", ok, err)
	}
	if w := warned(); w == nil || *w != 7 {
		t.Fatalf("marco após tentar regredir = %v, want 7", w)
	}

	// Token inexistente não grava e não é erro: nada a avisar.
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, 9999, 7); err != nil || ok {
		t.Errorf("MarkExpiryWarned em token inexistente = %v, %v; want false, nil", ok, err)
	}
}

// Editar a validade rearma os avisos: esticar o prazo deve fazer os marcos
// (30 e 7) valerem de novo, então SetExpiry limpa o marco já avisado.
func testTokenSetExpiryClearsWarning(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	dono := mustCreateUser(t, s, "dona")
	tok, _ := mustCreateToken(t, s, dono.ID, "deploy")
	prazo := epoch.Add(5 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, tok.ID, &prazo); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Tokens().MarkExpiryWarned(ctx, tok.ID, 7); err != nil || !ok {
		t.Fatalf("MarkExpiryWarned(7) = %v, %v; want true, nil", ok, err)
	}

	// Estica o prazo: os avisos recomeçam, então o marco é limpo.
	novo := epoch.Add(120 * 24 * time.Hour)
	if err := s.Tokens().SetExpiry(ctx, tok.ID, &novo); err != nil {
		t.Fatal(err)
	}

	got, err := s.Tokens().ListExpiring(ctx, epoch.Add(200*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, et := range got {
		if et.TokenID == tok.ID {
			if et.WarnedDays != nil {
				t.Errorf("WarnedDays = %v após editar a validade, want nil (avisos rearmados)", et.WarnedDays)
			}
			return
		}
	}
	t.Fatalf("token %d sumiu da listagem após editar a validade", tok.ID)
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

// Rotacionar emite um substituto e marca o antigo como rotacionado, sem
// revogá-lo: os dois seguem válidos (sobreposição) até o dono revogar o
// antigo, para a rotação não ter janela de corte.
func testTokenRotateKeepsBothValid(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	old, oldSecret := mustCreateToken(t, s, u.ID, "ci")

	novoSecret, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	novo := domain.APIToken{
		UserID: u.ID, Name: "ci (rotacionado)",
		Hash: novoSecret.Hash, Prefix: novoSecret.Prefix,
	}
	if err := s.Tokens().Rotate(ctx, old.ID, &novo); err != nil {
		t.Fatalf("Rotate returned unexpected error: %v", err)
	}
	if novo.ID == 0 || novo.CreatedAt.IsZero() {
		t.Errorf("Rotate não preencheu o token substituto: %+v", novo)
	}

	// O antigo segue autenticando, agora marcado como rotacionado.
	gotOld, err := s.Tokens().GetByHash(ctx, domain.HashToken(oldSecret.Secret))
	if err != nil {
		t.Fatalf("o token antigo deixou de autenticar após a rotação: %v", err)
	}
	if gotOld.RotatedAt == nil {
		t.Error("o token antigo não ficou marcado como rotacionado")
	}

	// O substituto também autentica.
	if _, err := s.Tokens().GetByHash(ctx, novoSecret.Hash); err != nil {
		t.Errorf("o token substituto não autentica: %v", err)
	}

	// Rotacionar um token inexistente é ErrNotFound, sem gravar o substituto.
	antes := len(mustListTokens(t, s, u.ID))
	outro := domain.APIToken{UserID: u.ID, Name: "x", Hash: "h-inexistente", Prefix: "upw_x"}
	if err := s.Tokens().Rotate(ctx, 9999, &outro); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Rotate de token inexistente = %v, want ErrNotFound", err)
	}
	if depois := len(mustListTokens(t, s, u.ID)); depois != antes {
		t.Errorf("a rotação recusada gravou o substituto: antes %d, depois %d", antes, depois)
	}
}

// SuccessorUsed é o gatilho da detecção de reuso: só acusa quando o token
// substituto já foi usado, ou seja, quando o cliente migrou para o segredo
// novo e usar o antigo passa a ser sinal de vazamento.
func testTokenSuccessorUsed(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	u := mustCreateUser(t, s, "admin")
	old, _ := mustCreateToken(t, s, u.ID, "ci")

	novoSecret, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	novo := domain.APIToken{UserID: u.ID, Name: "ci2", Hash: novoSecret.Hash, Prefix: novoSecret.Prefix}
	if err := s.Tokens().Rotate(ctx, old.ID, &novo); err != nil {
		t.Fatal(err)
	}

	// Antes de o substituto ser usado, não há reuso a detectar.
	if used, err := s.Tokens().SuccessorUsed(ctx, old.ID); err != nil || used {
		t.Fatalf("SuccessorUsed antes do uso = %v, %v; want false, nil", used, err)
	}

	// Usar o substituto arma a detecção.
	if err := s.Tokens().TouchLastUsed(ctx, novo.ID, epoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if used, err := s.Tokens().SuccessorUsed(ctx, old.ID); err != nil || !used {
		t.Fatalf("SuccessorUsed após o uso = %v, %v; want true, nil", used, err)
	}

	// Um token sem substituto nunca acusa reuso.
	solo, _ := mustCreateToken(t, s, u.ID, "solo")
	if used, err := s.Tokens().SuccessorUsed(ctx, solo.ID); err != nil || used {
		t.Fatalf("SuccessorUsed sem sucessor = %v, %v; want false, nil", used, err)
	}
}

// Remover a conta corta os tokens dela na hora, por cascata: quem perdeu o
// acesso não deixa credencial de pé esperando o vencimento. Diferente de uma
// revogação, o registro sai junto — não há conta a que prender o retido.
func testTokenCascadesOnUserDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	mustCreateUser(t, s, "fica") // outro administrador, para não ser o último
	u := mustCreateUser(t, s, "demitido")
	tok, secret := mustCreateToken(t, s, u.ID, "ci")

	if err := s.Users().Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}

	if _, err := s.Tokens().GetByHash(ctx, domain.HashToken(secret.Secret)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("o token sobreviveu à remoção da conta: %v", err)
	}
	if _, err := s.Tokens().Get(ctx, tok.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("o registro do token sobreviveu à remoção da conta: %v", err)
	}
}

func mustListTokens(t *testing.T, s store.Store, userID int64) []domain.APIToken {
	t.Helper()
	list, err := s.Tokens().List(context.Background(), userID)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	return list
}

// ---------- papéis ----------

func testUserRoleRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	u := domain.User{Username: "observador", Role: domain.RoleViewer, PasswordHash: "x"}
	if err := s.Users().Create(ctx, &u); err != nil {
		t.Fatalf("criando: %v", err)
	}

	volta, err := s.Users().Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	// O papel decide o que a conta pode fazer. Se ele não sobrevivesse à
	// ida e volta, um observador viraria administrador no próximo login.
	if volta.Role != domain.RoleViewer {
		t.Fatalf("papel divergiu: %s", volta.Role)
	}
}

func testUserListBringsEveryone(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	mustCreateUser(t, s, "ana")
	mustCreateUser(t, s, "bruno")

	contas, err := s.Users().List(ctx)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(contas) != 2 {
		t.Fatalf("esperava 2 contas, vieram %d", len(contas))
	}
	// Nenhuma listagem devolve o hash: a tela de contas mostra quem
	// existe, não material para ataque offline.
	for _, c := range contas {
		if c.Username == "" {
			t.Error("conta veio sem nome de usuário")
		}
	}
}

func testUserDeleteRemovesSessions(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	// Outro administrador fica: apagar o último de escopo total é recusado.
	mustCreateUser(t, s, "fica")
	u := mustCreateUser(t, s, "demitido")
	sessao := domain.Session{
		Hash: "hash-da-sessao", UserID: u.ID,
		CreatedAt: epoch, ExpiresAt: epoch.Add(24 * time.Hour),
	}
	if err := s.Sessions().Create(ctx, sessao); err != nil {
		t.Fatalf("criando sessão: %v", err)
	}

	if err := s.Users().Delete(ctx, u.ID); err != nil {
		t.Fatalf("apagando conta: %v", err)
	}

	// A sessão precisa sair junto: quem perdeu o acesso não pode
	// continuar dentro até o cookie vencer.
	if _, err := s.Sessions().Get(ctx, sessao.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sessão sobreviveu à remoção da conta: %v", err)
	}
}

func testUserCountByRoleSeparates(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	mustCreateUser(t, s, "admin-1")
	mustCreateUser(t, s, "admin-2")

	observador := domain.User{Username: "so-olha", Role: domain.RoleViewer, PasswordHash: "x"}
	if err := s.Users().Create(ctx, &observador); err != nil {
		t.Fatalf("criando observador: %v", err)
	}

	// É esta contagem que impede remover o último administrador e deixar
	// a instalação sem ninguém capaz de criar outro.
	admins, err := s.Users().CountByRole(ctx, domain.RoleAdmin)
	if err != nil {
		t.Fatalf("contando: %v", err)
	}
	if admins != 2 {
		t.Fatalf("esperava 2 administradores, vieram %d", admins)
	}

	viewers, err := s.Users().CountByRole(ctx, domain.RoleViewer)
	if err != nil {
		t.Fatalf("contando: %v", err)
	}
	if viewers != 1 {
		t.Fatalf("esperava 1 observador, vieram %d", viewers)
	}
}
