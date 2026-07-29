package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

func TestUserValidateAcceptsWellFormedUser(t *testing.T) {
	u := domain.User{Username: "admin"}

	if err := u.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestUserValidateRejectsEmptyUsername(t *testing.T) {
	u := domain.User{Username: "   "}

	assertFieldError(t, u.Validate(), "username")
}

func TestUserValidateRejectsOverlongUsername(t *testing.T) {
	u := domain.User{Username: strings.Repeat("a", 200)}

	assertFieldError(t, u.Validate(), "username")
}

// Senha curta é o vetor mais explorado numa interface exposta à internet;
// o mínimo precisa ser recusado no cadastro, não apenas sugerido.
func TestValidatePasswordRejectsShortPassword(t *testing.T) {
	err := domain.ValidatePassword(strings.Repeat("a", domain.MinPasswordLength-1))

	if err == nil {
		t.Fatal("ValidatePassword of a short password returned nil error, want an error")
	}
}

func TestValidatePasswordAcceptsMinimumLength(t *testing.T) {
	if err := domain.ValidatePassword(strings.Repeat("a", domain.MinPasswordLength)); err != nil {
		t.Errorf("ValidatePassword returned unexpected error: %v", err)
	}
}

// bcrypt trunca silenciosamente em 72 bytes: uma senha longa aceita sem
// aviso teria seu final ignorado, e o usuário acreditaria numa força que
// não existe.
func TestValidatePasswordRejectsBeyondBcryptLimit(t *testing.T) {
	err := domain.ValidatePassword(strings.Repeat("a", 73))

	if err == nil {
		t.Fatal("ValidatePassword beyond the bcrypt limit returned nil error, want an error")
	}
}

func TestSetPasswordStoresHashNotPlaintext(t *testing.T) {
	var u domain.User
	const password = "uma-senha-bem-longa"

	if err := u.SetPassword(password); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}

	if u.PasswordHash == "" {
		t.Fatal("PasswordHash is empty after SetPassword")
	}
	if strings.Contains(u.PasswordHash, password) {
		t.Error("PasswordHash contains the plaintext password")
	}
}

func TestCheckPasswordAcceptsCorrectPassword(t *testing.T) {
	var u domain.User
	if err := u.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}

	if !u.CheckPassword("uma-senha-bem-longa") {
		t.Error("CheckPassword rejected the correct password")
	}
}

func TestCheckPasswordRejectsWrongPassword(t *testing.T) {
	var u domain.User
	if err := u.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}

	if u.CheckPassword("outra-senha-qualquer") {
		t.Error("CheckPassword accepted a wrong password")
	}
}

// Cada gravação usa sal novo, então o mesmo texto produz hashes
// diferentes: sem isso, senhas iguais seriam identificáveis no banco.
func TestSetPasswordUsesFreshSalt(t *testing.T) {
	var first, second domain.User
	if err := first.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	if err := second.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}

	if first.PasswordHash == second.PasswordHash {
		t.Error("the same password produced identical hashes, want a fresh salt each time")
	}
}

func TestCheckPasswordOnUserWithoutHashFails(t *testing.T) {
	var u domain.User

	if u.CheckPassword("qualquer-coisa") {
		t.Error("CheckPassword succeeded on a user with no password set")
	}
}

// A senha nunca pode sair pela API. Marshal precisa omitir o hash mesmo
// que alguém serialize o usuário inteiro por descuido.
func TestUserJSONOmitsPasswordHash(t *testing.T) {
	var u domain.User
	u.Username = "admin"
	if err := u.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}

	encoded, err := u.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON returned unexpected error: %v", err)
	}

	if strings.Contains(string(encoded), "password") {
		t.Errorf("serialised user mentions the password: %s", encoded)
	}
	if strings.Contains(string(encoded), u.PasswordHash) {
		t.Errorf("serialised user leaks the password hash: %s", encoded)
	}
}

// ---------- tokens ----------

func TestNewTokenProducesUnpredictableSecret(t *testing.T) {
	first, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}
	second, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}

	if first.Secret == second.Secret {
		t.Error("NewToken produced the same secret twice")
	}
	if len(first.Secret) < 32 {
		t.Errorf("secret has %d characters, want at least 32", len(first.Secret))
	}
}

// O prefixo permite identificar o token na listagem sem guardar o segredo.
func TestNewTokenExposesPrefixOfTheSecret(t *testing.T) {
	tok, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}

	if !strings.HasPrefix(tok.Secret, tok.Prefix) {
		t.Errorf("prefix %q is not the beginning of secret %q", tok.Prefix, tok.Secret)
	}
	if len(tok.Prefix) >= len(tok.Secret) {
		t.Error("prefix is as long as the secret, want only a recognisable fragment")
	}
}

// Tokens são segredos de alta entropia, então hash rápido basta e é
// obrigatório: bcrypt a cada requisição autenticada por token tornaria a
// API lenta o suficiente para virar um vetor de negação de serviço.
func TestHashTokenIsDeterministic(t *testing.T) {
	a := domain.HashToken("um-segredo-qualquer")
	b := domain.HashToken("um-segredo-qualquer")

	if a != b {
		t.Errorf("HashToken produced %q then %q for the same input", a, b)
	}
	if a == "um-segredo-qualquer" {
		t.Error("HashToken returned the input unchanged")
	}
}

func TestHashTokenDiffersBetweenSecrets(t *testing.T) {
	if domain.HashToken("segredo-a") == domain.HashToken("segredo-b") {
		t.Error("different secrets produced the same hash")
	}
}

func TestNewTokenHashMatchesItsSecret(t *testing.T) {
	tok, err := domain.NewToken()
	if err != nil {
		t.Fatalf("NewToken returned unexpected error: %v", err)
	}

	if tok.Hash != domain.HashToken(tok.Secret) {
		t.Error("the token hash does not match its own secret")
	}
}

func TestAPITokenExpiry(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		expires *time.Time
		want    bool
	}{
		{"sem validade nunca expira", nil, false},
		{"validade no futuro", ptr(now.Add(time.Hour)), false},
		{"validade no passado", ptr(now.Add(-time.Hour)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := domain.APIToken{ExpiresAt: tt.expires}
			if got := tok.Expired(now); got != tt.want {
				t.Errorf("Expired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

// O segredo bruto só existe no momento da criação; a listagem mostra o
// prefixo, e nada permite reconstruí-lo.
func TestAPITokenJSONOmitsHash(t *testing.T) {
	tok := domain.APIToken{ID: 1, Name: "ci", Prefix: "upw_abcd"}
	tok.Hash = domain.HashToken("segredo")

	encoded, err := tok.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON returned unexpected error: %v", err)
	}

	if strings.Contains(string(encoded), tok.Hash) {
		t.Errorf("serialised token leaks its hash: %s", encoded)
	}
	if !strings.Contains(string(encoded), "upw_abcd") {
		t.Errorf("serialised token omits the prefix, which is what identifies it: %s", encoded)
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	if (domain.Session{ExpiresAt: now.Add(time.Hour)}).Expired(now) {
		t.Error("a session valid for another hour reported as expired")
	}
	if !(domain.Session{ExpiresAt: now.Add(-time.Second)}).Expired(now) {
		t.Error("an expired session reported as valid")
	}
}
