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

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
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

	// ErrLastAdmin recusa remover ou rebaixar o último administrador.
	//
	// Sem esta guarda, um clique deixaria a instalação sem ninguém capaz
	// de administrar, e a única saída seria mexer no banco à mão.
	ErrLastAdmin = errors.New("auth: é preciso manter ao menos um administrador com acesso a todos os grupos")

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

	// A primeira conta é sempre administradora: é ela que vai criar as
	// demais, e uma instalação que nasce sem ninguém capaz de administrar
	// não teria como sair desse estado.
	u := domain.User{Username: strings.TrimSpace(username), Role: domain.RoleAdmin}
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

// CreateUser cadastra uma conta com o papel e o escopo indicados.
//
// Separado de CreateInitialAdmin de propósito: aquele é o assistente de
// primeiro acesso e recusa a segunda chamada; este exige que já exista
// alguém administrando, e é chamado por essa pessoa. Se quem chama pode
// conceder o escopo pedido é decidido antes, por quem conhece o escopo
// dela (ver domain.Access.Grants).
func (s *Service) CreateUser(
	ctx context.Context, username, password string, role domain.Role, scope domain.Scope,
) (domain.User, error) {
	u := domain.User{Username: strings.TrimSpace(username), Role: role, Scope: scope.Normalize()}
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

// SetAccess troca o papel e, quando informado, o escopo de uma conta.
//
// Escopo nulo mantém o atual: trocar o papel não pode alargar o que a
// conta alcança porque um campo ficou de fora do pedido.
//
// Recusa tirar do último administrador com escopo total o papel ou o
// escopo. Administrador restrito não cria grupo de topo nem mexe em canal
// ou página pública; sem ninguém com escopo total, a instalação ficaria
// sem quem faça isso, e a única saída seria mexer no banco à mão.
func (s *Service) SetAccess(ctx context.Context, id int64, role domain.Role, scope *domain.Scope) (domain.User, error) {
	u, err := s.store.Users().Get(ctx, id)
	if err != nil {
		return domain.User{}, err
	}

	novo := u
	novo.Role = role
	if scope != nil {
		novo.Scope = scope.Normalize()
	}
	if err := novo.Validate(); err != nil {
		return domain.User{}, err
	}

	if fullAdmin(u) && !fullAdmin(novo) {
		if err := s.requireAnotherFullAdmin(ctx); err != nil {
			return domain.User{}, err
		}
	}

	if err := s.store.Users().Update(ctx, novo); err != nil {
		return domain.User{}, err
	}
	return novo, nil
}

// DeleteUser remove a conta, recusando o último administrador com escopo
// total.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	u, err := s.store.Users().Get(ctx, id)
	if err != nil {
		return err
	}
	if fullAdmin(u) {
		if err := s.requireAnotherFullAdmin(ctx); err != nil {
			return err
		}
	}
	return s.store.Users().Delete(ctx, id)
}

// fullAdmin é a conta que administra a instalação inteira.
func fullAdmin(u domain.User) bool {
	return u.Role == domain.RoleAdmin && !u.Scope.Restricted
}

// requireAnotherFullAdmin confere que sobra alguém administrando tudo.
//
// Varre a lista em vez de contar no banco: contas são poucas, e a regra
// "administrador com escopo total" fica escrita aqui uma vez, em vez de
// repetida em SQL.
func (s *Service) requireAnotherFullAdmin(ctx context.Context) error {
	contas, err := s.store.Users().List(ctx)
	if err != nil {
		return err
	}
	n := 0
	for _, c := range contas {
		if fullAdmin(c) {
			n++
		}
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}
