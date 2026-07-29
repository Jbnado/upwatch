// Package auth cuida de identidade e credenciais.
//
// Vive separado dos handlers HTTP para que as regras de segurança sejam
// verificáveis sem subir servidor: bloqueio por tentativas, expiração de
// sessão e revogação de token são exercitados diretamente.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

const (
	// DefaultSessionTTL é a validade de um login na interface.
	DefaultSessionTTL = 7 * 24 * time.Hour

	// MaxLoginAttempts é quantas falhas seguidas uma conta tolera antes de
	// ser bloqueada temporariamente.
	MaxLoginAttempts = 5

	// LockoutWindow é a duração do bloqueio.
	//
	// Bloqueio temporário em vez de permanente: o permanente transformaria
	// tentativas de login erradas numa forma de negar acesso ao dono da
	// conta.
	LockoutWindow = 15 * time.Minute
)

var (
	// ErrInvalidCredentials cobre tanto senha errada quanto conta
	// inexistente. Um erro só, de propósito: distinguir os dois permitiria
	// descobrir quais contas existem antes de atacar a senha.
	ErrInvalidCredentials = errors.New("auth: credenciais inválidas")

	// ErrUnauthenticated indica sessão ou token ausente, vencido ou
	// desconhecido.
	ErrUnauthenticated = errors.New("auth: não autenticado")

	// ErrTooManyAttempts indica conta temporariamente bloqueada.
	ErrTooManyAttempts = errors.New("auth: tentativas demais; tente novamente mais tarde")

	// ErrSetupComplete indica que o assistente de primeiro acesso já foi
	// usado.
	ErrSetupComplete = errors.New("auth: a instalação já possui uma conta")
)

// Options configura o serviço. Campos zerados assumem o padrão.
type Options struct {
	SessionTTL time.Duration
	Clock      clock.Clock
}

// Service reúne as operações de identidade.
type Service struct {
	store      store.MetadataStore
	clock      clock.Clock
	sessionTTL time.Duration

	mu       sync.Mutex
	failures map[string]*attemptRecord
}

// attemptRecord acompanha as falhas de uma conta.
type attemptRecord struct {
	count      int
	lockedTill time.Time
}

// New cria o serviço de autenticação.
func New(s store.MetadataStore, opts Options) *Service {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = DefaultSessionTTL
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	return &Service{
		store:      s,
		clock:      opts.Clock,
		sessionTTL: opts.SessionTTL,
		failures:   make(map[string]*attemptRecord),
	}
}

// ---------- primeiro acesso ----------

// NeedsSetup informa se a instalação ainda não tem conta.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	n, err := s.store.Users().Count(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// CreateInitialAdmin cria a primeira conta.
//
// Só funciona enquanto não existir nenhuma: o assistente não é
// autenticado, e mantê-lo disponível deixaria qualquer visitante criar uma
// conta e tomar a instalação.
func (s *Service) CreateInitialAdmin(ctx context.Context, username, password string) (domain.User, error) {
	need, err := s.NeedsSetup(ctx)
	if err != nil {
		return domain.User{}, err
	}
	if !need {
		return domain.User{}, ErrSetupComplete
	}

	u := domain.User{Username: strings.TrimSpace(username)}
	if err := u.Validate(); err != nil {
		return domain.User{}, err
	}
	if err := u.SetPassword(password); err != nil {
		return domain.User{}, err
	}
	if err := s.store.Users().Create(ctx, &u); err != nil {
		return domain.User{}, err
	}
	return u, nil
}

// ---------- login ----------

// Login confere as credenciais e abre uma sessão, devolvendo o segredo que
// vai no cookie.
func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	username = strings.TrimSpace(username)

	if s.locked(username) {
		return "", ErrTooManyAttempts
	}

	u, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Compara contra um hash descartável mesmo sem usuário, para o
			// tempo de resposta não denunciar quais contas existem.
			var decoy domain.User
			decoy.CheckPassword(password)

			s.recordFailure(username)
			return "", ErrInvalidCredentials
		}
		return "", err
	}

	if !u.CheckPassword(password) {
		s.recordFailure(username)
		return "", ErrInvalidCredentials
	}
	s.clearFailures(username)

	return s.openSession(ctx, u.ID)
}

// openSession grava uma sessão nova e devolve seu segredo.
func (s *Service) openSession(ctx context.Context, userID int64) (string, error) {
	tok, err := domain.NewToken()
	if err != nil {
		return "", err
	}

	now := s.clock.Now().UTC()
	sess := domain.Session{
		Hash:      tok.Hash,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL),
	}
	if err := s.store.Sessions().Create(ctx, sess); err != nil {
		return "", err
	}
	return tok.Secret, nil
}

// Logout encerra a sessão.
func (s *Service) Logout(ctx context.Context, secret string) error {
	return s.store.Sessions().Delete(ctx, domain.HashToken(secret))
}

// AuthenticateSession resolve o segredo do cookie na conta correspondente.
func (s *Service) AuthenticateSession(ctx context.Context, secret string) (domain.User, error) {
	if secret == "" {
		return domain.User{}, ErrUnauthenticated
	}

	sess, err := s.store.Sessions().Get(ctx, domain.HashToken(secret))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, ErrUnauthenticated
		}
		return domain.User{}, err
	}
	if sess.Expired(s.clock.Now()) {
		return domain.User{}, ErrUnauthenticated
	}

	u, err := s.store.Users().Get(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, ErrUnauthenticated
		}
		return domain.User{}, err
	}
	return u, nil
}

// SweepExpiredSessions remove as sessões vencidas.
func (s *Service) SweepExpiredSessions(ctx context.Context) (int64, error) {
	return s.store.Sessions().DeleteExpired(ctx, s.clock.Now())
}

// ---------- senha ----------

// ChangePassword troca a senha e encerra as sessões abertas.
//
// Derrubar as sessões é parte do propósito: trocar a senha porque ela pode
// ter vazado não adianta se quem a obteve continuar logado.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	u, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return err
	}
	if !u.CheckPassword(current) {
		return ErrInvalidCredentials
	}
	if err := u.SetPassword(next); err != nil {
		return err
	}
	if err := s.store.Users().Update(ctx, u); err != nil {
		return err
	}
	return s.store.Sessions().DeleteByUser(ctx, userID)
}

// ---------- tokens ----------

// IssueToken cria uma credencial programática e devolve o segredo.
//
// O segredo só existe neste retorno: depois disso apenas o hash permanece,
// e nem o operador consegue recuperá-lo.
func (s *Service) IssueToken(
	ctx context.Context,
	userID int64,
	name string,
	expiresAt *time.Time,
) (domain.APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return domain.APIToken{}, "", fmt.Errorf("auth: o token precisa de um nome")
	}

	secret, err := domain.NewToken()
	if err != nil {
		return domain.APIToken{}, "", err
	}

	tok := domain.APIToken{
		UserID: userID, Name: name,
		Hash: secret.Hash, Prefix: secret.Prefix, ExpiresAt: expiresAt,
	}
	if err := s.store.Tokens().Create(ctx, &tok); err != nil {
		return domain.APIToken{}, "", err
	}
	return tok, secret.Secret, nil
}

// AuthenticateToken resolve um segredo de API na conta dona.
func (s *Service) AuthenticateToken(ctx context.Context, secret string) (domain.User, error) {
	if secret == "" {
		return domain.User{}, ErrUnauthenticated
	}

	tok, err := s.store.Tokens().GetByHash(ctx, domain.HashToken(secret))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, ErrUnauthenticated
		}
		return domain.User{}, err
	}

	now := s.clock.Now()
	if tok.Expired(now) {
		return domain.User{}, ErrUnauthenticated
	}

	u, err := s.store.Users().Get(ctx, tok.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.User{}, ErrUnauthenticated
		}
		return domain.User{}, err
	}

	// Registrar o uso é o que permite identificar credenciais esquecidas
	// antes de revogá-las. Falhar aqui não invalida a autenticação.
	_ = s.store.Tokens().TouchLastUsed(ctx, tok.ID, now)

	return u, nil
}

// ListTokens devolve os tokens da conta.
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]domain.APIToken, error) {
	return s.store.Tokens().List(ctx, userID)
}

// RevokeToken apaga um token da própria conta.
//
// A checagem de dono impede que uma conta revogue credencial de outra, o
// que seria escalonamento de privilégio assim que houver mais de uma.
func (s *Service) RevokeToken(ctx context.Context, userID, tokenID int64) error {
	owned, err := s.store.Tokens().List(ctx, userID)
	if err != nil {
		return err
	}
	for _, tok := range owned {
		if tok.ID == tokenID {
			return s.store.Tokens().Delete(ctx, tokenID)
		}
	}
	return fmt.Errorf("token %d: %w", tokenID, store.ErrNotFound)
}

// ---------- bloqueio por tentativas ----------

// locked informa se a conta está temporariamente bloqueada.
func (s *Service) locked(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.failures[username]
	if !ok {
		return false
	}
	// Ainda acumulando: o bloqueio só existe depois de atingido o limite, e
	// descartar o histórico aqui zeraria a contagem a cada tentativa —
	// deixando o limitador sem efeito algum.
	if rec.lockedTill.IsZero() {
		return false
	}
	if s.clock.Now().Before(rec.lockedTill) {
		return true
	}
	// Janela vencida: o histórico é descartado para o bloqueio não virar
	// permanente.
	delete(s.failures, username)
	return false
}

func (s *Service) recordFailure(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.failures[username]
	if !ok {
		rec = &attemptRecord{}
		s.failures[username] = rec
	}
	rec.count++
	if rec.count >= MaxLoginAttempts {
		rec.lockedTill = s.clock.Now().Add(LockoutWindow)
	}
}

// clearFailures zera o histórico após um login bem sucedido, para quem
// errou algumas vezes não ficar a poucas tentativas de bloqueio.
func (s *Service) clearFailures(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, username)
}
