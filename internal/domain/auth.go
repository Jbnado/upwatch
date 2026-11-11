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
	// MinPasswordHashCost é o piso aceito na configuração da instalação.
	MinPasswordHashCost = 12
	// MaxPasswordHashCost limita o trabalho por senha a um custo operacional seguro.
	MaxPasswordHashCost = 14

	// MinPasswordLength é o mínimo aceito.
	MinPasswordLength = 12

	// maxPasswordLength é o limite do bcrypt. Ele trunca silenciosamente
	// em 72 bytes, então aceitar mais daria ao usuário a impressão de uma
	// senha forte cujo final é simplesmente ignorado.
	maxPasswordLength = 72

	// MaxUsernameLength limita o nome de usuário. Exportado porque o login
	// recusa, antes de qualquer conferência, o nome que passa disso.
	MaxUsernameLength = 64

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

	// Scope decide sobre quais grupos o papel vale. Ver scope.go.
	Scope Scope `json:"scope"`

	// PasswordHash nunca sai pela API; MarshalJSON o omite.
	PasswordHash string `json:"-"`

	// TOTPEnabled informa se a conta tem a verificação em duas etapas
	// ligada. Só leitura: quem grava é o fluxo de cadastro (ver
	// store.TwoFactorRepo), nunca a gravação da conta.
	TOTPEnabled bool `json:"totp_enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes da conta.
func (u User) Validate() error {
	name := strings.TrimSpace(u.Username)
	if name == "" {
		return invalid("username", "não pode ser vazio")
	}
	if len(name) > MaxUsernameLength {
		return invalid("username", fmt.Sprintf("não pode passar de %d caracteres", MaxUsernameLength))
	}
	if !u.Role.Valid() {
		return invalid("role", "papel desconhecido")
	}
	return u.Scope.Validate()
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
var PasswordHashCost = MinPasswordHashCost

// NeedsRehash informa se um hash válido usa custo abaixo do configurado.
// Um custo superior é preservado mesmo depois de uma redução na configuração.
func (u User) NeedsRehash() bool {
	cost, err := bcrypt.Cost([]byte(u.PasswordHash))
	return err == nil && cost < PasswordHashCost
}

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
		ID          int64     `json:"id"`
		Username    string    `json:"username"`
		Role        Role      `json:"role"`
		Scope       Scope     `json:"scope"`
		TOTPEnabled bool      `json:"totp_enabled"`
		CreatedAt   time.Time `json:"created_at"`
		UpdatedAt   time.Time `json:"updated_at"`
	}
	return json.Marshal(alias{
		ID: u.ID, Username: u.Username, Role: u.Role, Scope: u.Scope,
		TOTPEnabled: u.TOTPEnabled, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
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

	// RotatedAt marca que o token foi rotacionado: segue válido na janela de
	// sobreposição, mas há um sucessor emitido no lugar dele. Nulo é um
	// token que nunca foi rotacionado.
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
}

// Expired informa se o token já venceu. Sem validade, nunca vence.
func (t APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// Rotated informa se o token já foi rotacionado.
func (t APIToken) Rotated() bool { return t.RotatedAt != nil }

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
		RotatedAt  *time.Time `json:"rotated_at,omitempty"`
	}
	return json.Marshal(alias{
		ID: t.ID, UserID: t.UserID, Name: t.Name, Prefix: t.Prefix,
		CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt,
		RotatedAt: t.RotatedAt,
	})
}

// TokenExpiryPolicy é a política de validade de tokens de API da instalação,
// aplicada na criação de um token novo.
type TokenExpiryPolicy struct {
	// MaxLifetime é o maior prazo de vida permitido a partir da criação.
	// Zero significa sem teto.
	MaxLifetime time.Duration

	// Require exige que todo token novo tenha data de expiração.
	Require bool
}

// CheckExpiry valida a expiração pedida para um token criado em now.
// expiresAt nil significa "sem expiração".
func (p TokenExpiryPolicy) CheckExpiry(expiresAt *time.Time, now time.Time) error {
	if expiresAt == nil {
		if p.Require {
			return invalid("expires_at", "a instalação exige data de expiração")
		}
		return nil
	}
	if !expiresAt.After(now) {
		return invalid("expires_at", "a data de expiração deve ser no futuro")
	}
	if p.MaxLifetime > 0 && expiresAt.After(now.Add(p.MaxLifetime)) {
		return invalid("expires_at", "a data de expiração excede o teto da instalação")
	}
	return nil
}

// CheckExpiryEdit valida a troca da expiração de um token já emitido e diz
// se a troca esticou o prazo, para o chamador registrar na auditoria.
//
// createdAt é a âncora do teto: o maior prazo de vida conta desde a
// criação, não desde agora. current é a expiração atual (nil = sem prazo) e
// desired a pedida (nil = sem prazo).
//
// Encurtar ou adicionar prazo é livre — é sempre mais restritivo que o que
// havia. Esticar (afastar o vencimento, e remover o prazo é o esticão
// máximo) respeita o teto e a exigência da instalação. Um prazo pedido
// precisa ser no futuro: para matar o token agora, revoga-se, não se põe
// uma data no passado.
func (p TokenExpiryPolicy) CheckExpiryEdit(createdAt time.Time, current, desired *time.Time, now time.Time) (extended bool, err error) {
	if desired != nil && !desired.After(now) {
		return false, invalid("expires_at", "a data de expiração deve ser no futuro")
	}

	// Esticar é afastar o vencimento, tratando "sem prazo" como infinito:
	// de um prazo para outro mais tarde, ou de um prazo para nenhum.
	extended = current != nil && (desired == nil || desired.After(*current))
	if !extended {
		return false, nil
	}

	if desired == nil {
		if p.Require {
			return true, invalid("expires_at", "a instalação exige data de expiração")
		}
		if p.MaxLifetime > 0 {
			return true, invalid("expires_at", "remover o prazo excede o teto da instalação")
		}
		return true, nil
	}
	if p.MaxLifetime > 0 && desired.After(createdAt.Add(p.MaxLifetime)) {
		return true, invalid("expires_at", "a data de expiração excede o teto da instalação")
	}
	return true, nil
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
