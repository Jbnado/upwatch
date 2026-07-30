package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// MinPasswordLength é o mínimo aceito.
	MinPasswordLength = 12

	// maxPasswordLength é o limite do bcrypt. Ele trunca silenciosamente
	// em 72 bytes, então aceitar mais daria ao usuário a impressão de uma
	// senha forte cujo final é simplesmente ignorado.
	maxPasswordLength = 72

	// maxUsernameLength limita o nome de usuário.
	maxUsernameLength = 64

	// tokenPrefixLength é quanto do segredo aparece na listagem, o
	// suficiente para o operador reconhecer qual token revogar.
	tokenPrefixLength = 12

	// tokenPrefix marca a origem do segredo, ajudando ferramentas de
	// varredura de credenciais a identificá-lo se vazar.
	tokenPrefix = "upw_"
)

// User é uma conta de acesso à interface.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`

	// Role decide o que a conta pode fazer. Ver role.go.
	Role Role `json:"role"`

	// PasswordHash nunca sai pela API; MarshalJSON o omite.
	PasswordHash string `json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes da conta.
func (u User) Validate() error {
	name := strings.TrimSpace(u.Username)
	if name == "" {
		return invalid("username", "não pode ser vazio")
	}
	if len(name) > maxUsernameLength {
		return invalid("username", fmt.Sprintf("não pode passar de %d caracteres", maxUsernameLength))
	}
	if !u.Role.Valid() {
		return invalid("role", "papel desconhecido")
	}
	return nil
}

// ValidatePassword confere o comprimento da senha.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return invalid("password", fmt.Sprintf("precisa de pelo menos %d caracteres", MinPasswordLength))
	}
	if len(password) > maxPasswordLength {
		return invalid("password", fmt.Sprintf("não pode passar de %d bytes", maxPasswordLength))
	}
	return nil
}

// PasswordHashCost é o custo do bcrypt.
//
// Ajustável porque o valor certo depende do hardware: o objetivo é que
// verificar uma senha demore o suficiente para inviabilizar força bruta,
// e o que era caro há cinco anos é barato hoje. Testes o reduzem para não
// pagar esse custo deliberado em cada caso.
var PasswordHashCost = bcrypt.DefaultCost

// SetPassword grava o hash da senha.
//
// Usa bcrypt porque senha é segredo de baixa entropia: o custo deliberado
// da função é o que torna inviável testar bilhões de candidatas caso o
// banco vaze.
func (u *User) SetPassword(password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), PasswordHashCost)
	if err != nil {
		return fmt.Errorf("domain: gerando hash da senha: %w", err)
	}
	u.PasswordHash = string(hash)
	return nil
}

// CheckPassword compara a senha oferecida com o hash guardado.
func (u User) CheckPassword(password string) bool {
	if u.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// MarshalJSON serializa a conta sem qualquer vestígio da senha.
func (u User) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID        int64     `json:"id"`
		Username  string    `json:"username"`
		Role      Role      `json:"role"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	return json.Marshal(alias{
		ID: u.ID, Username: u.Username, Role: u.Role,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	})
}

// Token é um segredo recém-criado, junto do que será persistido.
//
// Secret só existe neste momento: depois de gravado, apenas o hash
// permanece, e nem o operador consegue recuperá-lo.
type Token struct {
	Secret string
	Hash   string
	Prefix string
}

// NewToken gera um segredo imprevisível e seu hash.
func NewToken() (Token, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Token{}, fmt.Errorf("domain: gerando token: %w", err)
	}

	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return Token{
		Secret: secret,
		Hash:   HashToken(secret),
		Prefix: secret[:tokenPrefixLength],
	}, nil
}

// HashToken devolve o hash usado para guardar e comparar segredos.
//
// SHA-256 em vez de bcrypt de propósito. Um token tem entropia alta o
// bastante para dispensar o custo artificial contra força bruta, e esse
// custo seria pago a cada requisição autenticada — o que transformaria a
// autenticação por token num vetor de negação de serviço.
func HashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// APIToken é uma credencial de acesso programático.
type APIToken struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`

	// Prefix identifica o token na listagem sem revelar o segredo.
	Prefix string `json:"prefix"`

	// Hash nunca sai pela API; MarshalJSON o omite.
	Hash string `json:"-"`

	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Expired informa se o token já venceu. Sem validade, nunca vence.
func (t APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// MarshalJSON serializa o token sem o hash.
func (t APIToken) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID         int64      `json:"id"`
		UserID     int64      `json:"user_id"`
		Name       string     `json:"name"`
		Prefix     string     `json:"prefix"`
		CreatedAt  time.Time  `json:"created_at"`
		LastUsedAt *time.Time `json:"last_used_at,omitempty"`
		ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	}
	return json.Marshal(alias{
		ID: t.ID, UserID: t.UserID, Name: t.Name, Prefix: t.Prefix,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
	})
}

// Session é um login ativo na interface.
type Session struct {
	// Hash é o que fica no banco; o cookie carrega o segredo cru, de modo
	// que um vazamento do banco não conceda acesso.
	Hash      string
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Expired informa se a sessão já venceu.
func (s Session) Expired(now time.Time) bool { return now.After(s.ExpiresAt) }
