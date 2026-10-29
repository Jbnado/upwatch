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

	attempts *attempts

	decoyOnce sync.Once
	decoy     string
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
		attempts:   newAttempts(opts.Clock),
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

// Login confere as credenciais. Sem segundo fator, abre a sessão e
// devolve o segredo que vai no cookie; com ele, devolve só o desafio que
// CompleteLogin troca pela sessão.
//
// A senha errada responde igual com ou sem segundo fator: só quem acerta
// a senha descobre que a conta tem um.
func (s *Service) Login(ctx context.Context, username, password string) (LoginResult, error) {
	username = strings.TrimSpace(username)

	// Nome maior que o aceito no cadastro não é de conta nenhuma. Recusar
	// antes de tudo poupa o bcrypt e o registro de tentativas.
	if len(username) > domain.MaxUsernameLength {
		return LoginResult{}, ErrInvalidCredentials
	}

	att, err := s.attempts.reserve(username)
	if err != nil {
		return LoginResult{}, err
	}
	defer att.release()

	u, err := s.store.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Compara contra um hash de verdade mesmo sem usuário, para o
			// tempo de resposta não denunciar quais contas existem.
			s.checkDecoy(password)

			att.fail()
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}

	if !u.CheckPassword(password) {
		att.fail()
		return LoginResult{}, ErrInvalidCredentials
	}

	// As falhas só zeram quando o login termina. Zerar já na senha certa
	// deixaria quem a tem errar códigos sem nunca chegar ao bloqueio,
	// bastando digitar a senha de novo a cada quatro palpites.
	if u.TOTPEnabled {
		return s.openChallenge(ctx, u.ID)
	}
	att.succeed()

	secret, err := s.openSession(ctx, u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Session: secret}, nil
}

// checkDecoy faz a comparação de bcrypt que a conta inexistente não tem.
//
// O hash é gerado uma vez, no custo configurado: com um hash vazio a
// comparação voltaria na hora, e a diferença de tempo diria quais nomes
// são de conta.
func (s *Service) checkDecoy(password string) {
	s.decoyOnce.Do(func() {
		// Senha sorteada, que ninguém digita. Se o sorteio falhar o hash
		// fica vazio e a comparação só perde o disfarce; o login segue.
		var u domain.User
		if tok, err := domain.NewToken(); err == nil && u.SetPassword(tok.Secret) == nil {
			s.decoy = u.PasswordHash
		}
	})
	domain.User{PasswordHash: s.decoy}.CheckPassword(password)
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
	// A senha atual errada conta no bloqueio, como no login: uma sessão
	// roubada não pode virar banco de testes para adivinhar a senha.
	att, err := s.attempts.reserve(u.Username)
	if err != nil {
		return err
	}
	defer att.release()
	if !u.CheckPassword(current) {
		att.fail()
		return ErrInvalidCredentials
	}
	// Com segundo fator, a senha certa não zera as falhas: parte delas
	// pode ser de código, e a senha sozinha não prova a conta.
	if !u.TOTPEnabled {
		att.succeed()
	}
	if err := u.SetPassword(next); err != nil {
		return err
	}
	// Só a senha: regravar a conta lida acima devolveria o papel e o
	// escopo de antes, desfazendo uma revogação feita nesse meio-tempo.
	if err := s.store.Users().SetPasswordHash(ctx, userID, u.PasswordHash); err != nil {
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
//
// A recusa é do store, na mesma transação da escrita (ver
// store.UserRepo.SetAccess). Conferir aqui, antes, deixava dois
// rebaixamentos simultâneos verem "sobra o outro" e gravarem os dois.
func (s *Service) SetAccess(ctx context.Context, id int64, role domain.Role, scope *domain.Scope) (domain.User, error) {
	if !role.Valid() {
		return domain.User{}, &domain.ValidationError{Field: "role", Msg: "papel desconhecido"}
	}
	if scope != nil {
		normalizado := scope.Normalize()
		if err := normalizado.Validate(); err != nil {
			return domain.User{}, err
		}
		scope = &normalizado
	}

	if err := s.store.Users().SetAccess(ctx, id, role, scope); err != nil {
		return domain.User{}, lastAdmin(err)
	}
	return s.store.Users().Get(ctx, id)
}

// DeleteUser remove a conta, recusando o último administrador com escopo
// total — também no store, pelo mesmo motivo de SetAccess.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	return lastAdmin(s.store.Users().Delete(ctx, id))
}

// lastAdmin traduz a recusa do store para o erro deste pacote.
func lastAdmin(err error) error {
	if errors.Is(err, store.ErrLastFullAdmin) {
		return ErrLastAdmin
	}
	return err
}
