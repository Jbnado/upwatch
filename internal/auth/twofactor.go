package auth

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/totp"
)

const (
	// ChallengeTTL é quanto tempo a senha certa espera pelo código.
	// Suficiente para abrir o aplicativo no celular; curto o bastante para
	// um desafio esquecido numa aba não valer a tarde inteira.
	ChallengeTTL = 5 * time.Minute

	// MaxChallengeAttempts é quantos códigos um desafio aceita antes de
	// ser descartado. Somado ao bloqueio por conta, adivinhar um código de
	// seis dígitos deixa de ser questão de paciência.
	MaxChallengeAttempts = 5

	// RecoveryCodeCount é quantos códigos de recuperação a conta recebe.
	RecoveryCodeCount = 10
)

var (
	// ErrInvalidCode é o código do segundo fator recusado: errado, de fora
	// da janela, já usado ou código de recuperação gasto. Um erro só, pelo
	// mesmo motivo de ErrInvalidCredentials.
	ErrInvalidCode = errors.New("auth: código inválido")

	// ErrChallengeInvalid é o desafio de login desconhecido, vencido,
	// esgotado ou já usado. Quem recebe volta para a senha.
	ErrChallengeInvalid = errors.New("auth: verificação expirada; entre de novo com usuário e senha")

	// ErrTOTPEnabled recusa recomeçar o cadastro com o fator ligado:
	// trocar o segredo por cima pularia a prova que desligar exige.
	ErrTOTPEnabled = errors.New("auth: a verificação em duas etapas já está ligada")

	// ErrTOTPNotEnabled indica conta sem o fator ligado (ou só pendente,
	// conforme a operação).
	ErrTOTPNotEnabled = errors.New("auth: a verificação em duas etapas não está ligada")

	// ErrResetOwnTOTP recusa o administrador desligar o próprio fator pelo
	// caminho de outra conta, que não pede senha nem código.
	ErrResetOwnTOTP = errors.New("auth: para desligar a sua verificação em duas etapas, use os ajustes da sua conta")
)

// LoginResult é o desfecho de um login que conferiu a senha.
type LoginResult struct {
	// Session é o segredo do cookie, quando o login terminou.
	Session string
	// Challenge é o segredo do desafio, quando falta o segundo fator.
	Challenge          string
	ChallengeExpiresAt time.Time
}

// NeedsSecondFactor informa se o login parou à espera do código.
func (r LoginResult) NeedsSecondFactor() bool { return r.Challenge != "" }

// Enrollment é o que a pessoa leva para o aplicativo autenticador.
type Enrollment struct {
	Secret string
	URI    string
}

// TOTPStatus resume o segundo fator de uma conta.
type TOTPStatus struct {
	Enabled           bool
	Pending           bool
	RecoveryCodesLeft int
}

// openChallenge grava o desafio entre a senha e o código.
func (s *Service) openChallenge(ctx context.Context, userID int64) (LoginResult, error) {
	tok, err := domain.NewToken()
	if err != nil {
		return LoginResult{}, err
	}
	now := s.clock.Now().UTC()
	c := domain.LoginChallenge{
		Hash: tok.Hash, UserID: userID,
		CreatedAt: now, ExpiresAt: now.Add(ChallengeTTL),
	}
	if err := s.store.TwoFactor().CreateChallenge(ctx, c); err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Challenge: tok.Secret, ChallengeExpiresAt: c.ExpiresAt}, nil
}

// CompleteLogin confere o segundo fator de um desafio e abre a sessão.
//
// O código errado conta no mesmo bloqueio da senha errada. Sem isso, quem
// tem a senha teria cinco palpites por desafio e desafios à vontade.
// Completion é o desfecho de um segundo fator aceito.
type Completion struct {
	// Session é o segredo do cookie.
	Session string

	// Username é a conta que entrou. Vai no retorno porque quem registra
	// o evento não tem de onde tirá-la: a sessão só passa a existir neste
	// retorno, então não há conta no contexto da requisição.
	Username string

	// UsedRecoveryCode diz que o código veio da lista de recuperação, e
	// não do aplicativo autenticador — o segundo fator foi contornado.
	UsedRecoveryCode bool
}

func (s *Service) CompleteLogin(ctx context.Context, challenge, code string) (Completion, error) {
	if challenge == "" {
		return Completion{}, ErrChallengeInvalid
	}
	hash := domain.HashToken(challenge)

	c, err := s.store.TwoFactor().AttemptChallenge(ctx, hash, s.clock.Now(), MaxChallengeAttempts)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Completion{}, ErrChallengeInvalid
		}
		return Completion{}, err
	}
	u, err := s.store.Users().Get(ctx, c.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Completion{}, ErrChallengeInvalid
		}
		return Completion{}, err
	}
	att, err := s.attempts.reserve(u.Username)
	if err != nil {
		return Completion{}, err
	}
	defer att.release()

	usada, err := s.checkSecondFactor(ctx, u.ID, code)
	if err != nil {
		return Completion{}, err
	}
	if !usada.ok() {
		att.fail()
		return Completion{}, &InvalidCodeError{Username: u.Username}
	}

	// Consumir depois de conferir, e exigir que o consumo aconteça: dois
	// pedidos com códigos diferentes, ambos certos, abririam duas sessões
	// com o mesmo desafio.
	if err := s.store.TwoFactor().ConsumeChallenge(ctx, hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Completion{}, ErrChallengeInvalid
		}
		return Completion{}, err
	}
	att.succeed()
	session, err := s.openSession(ctx, u.ID)
	if err != nil {
		return Completion{}, err
	}
	return Completion{
		Session:          session,
		Username:         u.Username,
		UsedRecoveryCode: usada == provaRecuperação,
	}, nil
}

// InvalidCodeError é o código errado, sabendo de qual conta era o
// desafio.
//
// A conta vai para o registro de auditoria: código errado com a senha já
// na mão é o sinal mais forte de conta comprometida, e a linha não serve
// de nada sem dizer qual conta. O código digitado nunca entra aqui.
//
// Is faz errors.Is(err, ErrInvalidCode) continuar valendo, de modo que a
// resposta HTTP e quem já tratava o erro não mudam.
type InvalidCodeError struct {
	Username string
}

func (e *InvalidCodeError) Error() string { return ErrInvalidCode.Error() }

func (e *InvalidCodeError) Is(target error) bool { return target == ErrInvalidCode }

// checkSecondFactor confere um código TOTP ou de recuperação, gastando-o,
// e diz qual dos dois passou.
//
// Seis dígitos é TOTP; qualquer outra coisa é tentada como código de
// recuperação. Os dois formatos não se confundem, então a pessoa não
// precisa dizer qual digitou. Quem chama não repete essa decisão: ela
// mudaria aqui e continuaria errada do outro lado.
// prova diz qual segundo fator o código passou.
//
// Um tipo em vez de dois booleanos porque "recusado, mas de
// recuperação" não existe: o par deixaria essa combinação
// representável, e é dela que sairia uma linha de auditoria dizendo que
// alguém entrou quando ninguém entrou.
type prova int

const (
	// provaNenhuma é o código recusado.
	provaNenhuma prova = iota
	provaTOTP
	provaRecuperação
)

// ok informa se o código passou.
func (p prova) ok() bool { return p != provaNenhuma }

func (s *Service) checkSecondFactor(ctx context.Context, userID int64, code string) (prova, error) {
	f, err := s.store.TwoFactor().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return provaNenhuma, nil
	}
	if err != nil {
		return provaNenhuma, err
	}
	if !f.Enabled {
		return provaNenhuma, nil
	}

	if totp.LooksLikeCode(code) {
		step, ok := totp.Verify(f.Secret, code, s.clock.Now(), f.LastStep)
		if !ok {
			return provaNenhuma, nil
		}
		// A gravação do passo é que decide: entre a leitura acima e aqui,
		// outro pedido pode ter usado o mesmo código.
		gravou, err := s.store.TwoFactor().AdvanceStep(ctx, userID, step)
		if err != nil || !gravou {
			return provaNenhuma, err
		}
		return provaTOTP, nil
	}

	normalized := normalizeRecoveryCode(code)
	if normalized == "" {
		return provaNenhuma, nil
	}
	gastou, err := s.store.TwoFactor().UseRecoveryCode(ctx, userID, domain.HashToken(normalized), s.clock.Now().UTC())
	if err != nil || !gastou {
		return provaNenhuma, err
	}
	return provaRecuperação, nil
}

// ---------- cadastro ----------

// TOTPStatus informa se a conta tem o fator e quantos códigos sobram.
func (s *Service) TOTPStatus(ctx context.Context, userID int64) (TOTPStatus, error) {
	f, err := s.store.TwoFactor().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return TOTPStatus{}, nil
	}
	if err != nil {
		return TOTPStatus{}, err
	}
	st := TOTPStatus{Enabled: f.Enabled, Pending: !f.Enabled}
	if f.Enabled {
		if st.RecoveryCodesLeft, err = s.store.TwoFactor().RecoveryCodesLeft(ctx, userID); err != nil {
			return TOTPStatus{}, err
		}
	}
	return st, nil
}

// BeginTOTP sorteia um segredo e o guarda como pendente.
//
// Pendente até a confirmação: ligar na hora deixaria trancado fora quem
// fechou a tela antes de escanear o código.
//
// Pede a senha, contada no bloqueio. Com a sessão só, quem roubou o
// cookie cadastraria o próprio aplicativo, e a confirmação, que derruba
// as outras sessões, deixaria o dono de fora.
func (s *Service) BeginTOTP(ctx context.Context, userID int64, password string) (Enrollment, error) {
	u, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return Enrollment{}, err
	}
	// Antes da senha: com o fator ligado, a senha certa não pode zerar as
	// falhas, e recomeçar o cadastro seria recusado de qualquer jeito.
	if u.TOTPEnabled {
		return Enrollment{}, ErrTOTPEnabled
	}
	att, err := s.attempts.reserve(u.Username)
	if err != nil {
		return Enrollment{}, err
	}
	defer att.release()
	if !u.CheckPassword(password) {
		att.fail()
		return Enrollment{}, ErrInvalidCredentials
	}
	att.succeed()

	secret, err := totp.NewSecret()
	if err != nil {
		return Enrollment{}, err
	}
	if err := s.store.TwoFactor().SetPending(ctx, userID, secret, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return Enrollment{}, ErrTOTPEnabled
		}
		return Enrollment{}, err
	}
	return Enrollment{Secret: secret, URI: totp.URI(secret, u.Username)}, nil
}

// ConfirmTOTP liga o fator pendente com um código do aplicativo e devolve
// os códigos de recuperação, que só existem neste retorno.
//
// As outras credenciais da conta caem — sessões e tokens de API: uma
// aberta antes, talvez por quem roubou a senha, não pode sobreviver
// justamente à proteção nova, e o token menos ainda, porque é ele que o
// segundo fator não cobre. keep é o segredo da sessão de quem está
// ligando, que fica; vazio derruba todas.
func (s *Service) ConfirmTOTP(
	ctx context.Context, userID int64, atual Credential, code string,
) ([]string, error) {
	f, err := s.store.TwoFactor().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTOTPNotEnabled
	}
	if err != nil {
		return nil, err
	}
	if f.Enabled {
		return nil, ErrTOTPEnabled
	}

	step, ok := totp.Verify(f.Secret, code, s.clock.Now(), 0)
	if !ok {
		return nil, ErrInvalidCode
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	// Revogar antes de ligar. Na ordem inversa, uma revogação que falhe
	// deixa o fator ligado e descarta os dez códigos de recuperação no
	// retorno de erro: a conta passa a exigir um segundo fator cujos
	// códigos ninguém recebeu, e repetir responde que já está ligado.
	if err := s.revokeCredentials(ctx, userID, atual); err != nil {
		return nil, err
	}
	if err := s.store.TwoFactor().Enable(ctx, userID, step, hashes, s.clock.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTOTPEnabled
		}
		return nil, err
	}
	return codes, nil
}

// RegenerateRecoveryCodes troca os códigos de recuperação.
//
// Pede senha e código: com a sessão só, quem sentou num computador
// destravado sairia com dez segundos fatores no bolso.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64, password, code string) ([]string, error) {
	if err := s.proveAccount(ctx, userID, password, code); err != nil {
		return nil, err
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.store.TwoFactor().ReplaceRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// DisableTOTP desliga o próprio fator, com a mesma prova de
// RegenerateRecoveryCodes, e derruba as outras sessões como ConfirmTOTP.
func (s *Service) DisableTOTP(
	ctx context.Context, userID int64, atual Credential, password, code string,
) error {
	if err := s.proveAccount(ctx, userID, password, code); err != nil {
		return err
	}
	// Revogar antes de desligar, pelo mesmo motivo do ConfirmTOTP: o
	// Disable usa requireAffected, então um retry depois de desligar
	// responde "não está ligada" e a revogação nunca acontece.
	if err := s.revokeCredentials(ctx, userID, atual); err != nil {
		return err
	}
	if err := s.store.TwoFactor().Disable(ctx, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTOTPNotEnabled
		}
		return err
	}
	return nil
}

// ResetTOTP desliga o fator de outra conta, para quem perdeu o celular e
// os códigos de recuperação. Quem pode fazer isso é decidido antes (ver
// as rotas de contas); aqui só se recusa a própria conta.
//
// Todas as credenciais da conta caem, sessões e tokens de API. A pessoa
// entra de novo com a senha e, se a instalação exigir, cadastra o
// aplicativo novo nesse login; a automação dela é recadastrada, porque um
// reset de segundo fator também responde a incidente, e aí o token que
// sobrevivesse seria justamente o que o fator nunca cobriu.
func (s *Service) ResetTOTP(ctx context.Context, actorID, userID int64) error {
	if actorID == userID {
		return ErrResetOwnTOTP
	}
	if err := s.revokeCredentials(ctx, userID, Credential{}); err != nil {
		return err
	}
	if err := s.store.TwoFactor().Disable(ctx, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTOTPNotEnabled
		}
		return err
	}
	return nil
}

// ResetTOTPByUsername é a saída de quem não tem mais ninguém para pedir:
// o único administrador que perdeu o celular e os códigos. Roda pela
// linha de comando, com acesso ao banco, que é a prova que basta.
func (s *Service) ResetTOTPByUsername(ctx context.Context, username string) error {
	u, err := s.store.Users().GetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return err
	}
	if err := s.revokeCredentials(ctx, u.ID, Credential{}); err != nil {
		return err
	}
	if err := s.store.TwoFactor().Disable(ctx, u.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTOTPNotEnabled
		}
		return err
	}
	return nil
}

// SweepExpiredChallenges remove os desafios de login vencidos.
func (s *Service) SweepExpiredChallenges(ctx context.Context) (int64, error) {
	return s.store.TwoFactor().DeleteExpiredChallenges(ctx, s.clock.Now())
}

// proveAccount exige a senha e um código válidos da conta. As falhas
// contam no bloqueio, como no login: uma sessão roubada não pode virar
// banco de testes para adivinhar a senha.
func (s *Service) proveAccount(ctx context.Context, userID int64, password, code string) error {
	u, err := s.store.Users().Get(ctx, userID)
	if err != nil {
		return err
	}
	if !u.TOTPEnabled {
		return ErrTOTPNotEnabled
	}
	att, err := s.attempts.reserve(u.Username)
	if err != nil {
		return err
	}
	defer att.release()
	if !u.CheckPassword(password) {
		att.fail()
		return ErrInvalidCredentials
	}
	usada, err := s.checkSecondFactor(ctx, userID, code)
	if err != nil {
		return err
	}
	if !usada.ok() {
		att.fail()
		return ErrInvalidCode
	}
	att.succeed()
	return nil
}

// Credential é a credencial que está fazendo a mudança, e que por isso
// sobrevive a ela.
//
// Uma das duas, nunca as duas: a requisição chega por cookie ou por
// Bearer. Campos nomeados, e não duas strings em sequência, porque
// trocá-las de lugar preservaria a credencial errada sem o compilador
// reclamar — e a errada aqui é a que deveria cair.
type Credential struct {
	// Session é o segredo do cookie.
	Session string
	// Token é o segredo do token de API.
	Token string
}

// revokeCredentials corta o acesso da conta depois de uma mudança que
// existe para cortar acesso: trocar a senha, ligar, desligar ou resetar
// o segundo fator.
//
// Sessões e tokens de API no mesmo lugar porque esquecer os tokens foi
// exatamente o que aconteceu. Essas quatro ações derrubavam a sessão e
// deixavam de pé a credencial que faz tudo o que a interface faz, não
// vence por padrão e não passa pelo segundo fator: quem roubasse uma
// sessão de administrador emitia um token, e ele sobrevivia à troca de
// senha e ao segundo fator ligado depois — com a vítima vendo o painel
// dizer que estava resolvido.
//
// A credencial de atual sobrevive, nos dois tipos. Dizer "quem está na
// interface não usa token" era falso: authenticate aceita Bearer em
// toda rota autenticada, e ligar o segundo fator por token devolvia 200
// com os códigos de recuperação depois de matar o próprio chamador.
//
// Token antes de sessão: se uma das duas falhar, é melhor sobrar a
// sessão, que vence e passa pelo segundo fator no próximo login, do que
// o token, que não faz nem uma coisa nem outra.
func (s *Service) revokeCredentials(ctx context.Context, userID int64, atual Credential) error {
	if atual.Token == "" {
		if err := s.store.Tokens().DeleteByUser(ctx, userID); err != nil {
			return err
		}
	} else if err := s.store.Tokens().DeleteOthers(
		ctx, userID, domain.HashToken(atual.Token)); err != nil {
		return err
	}
	return s.revokeOthers(ctx, userID, atual.Session)
}

// revokeOthers derruba as sessões da conta menos a de keep.
func (s *Service) revokeOthers(ctx context.Context, userID int64, keep string) error {
	if keep == "" {
		return s.store.Sessions().DeleteByUser(ctx, userID)
	}
	return s.store.Sessions().DeleteOthers(ctx, userID, domain.HashToken(keep))
}

// ---------- códigos de recuperação ----------

// recoveryEncoding escreve os códigos em minúsculas e sem os caracteres
// que se confundem ao copiar de um papel: nem 0 e o, nem 1 e l.
var recoveryEncoding = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

// recoveryCodeBytes dá 80 bits por código: o hash é SHA-256 sem custo
// (ver domain.HashToken), e com menos entropia um banco vazado deixaria
// recuperar os códigos por força bruta.
const recoveryCodeBytes = 10

// newRecoveryCodes sorteia os códigos e devolve, à parte, o que se grava.
func newRecoveryCodes() (codes, hashes []string, err error) {
	for range RecoveryCodeCount {
		buf := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, fmt.Errorf("auth: sorteando código de recuperação: %w", err)
		}
		raw := recoveryEncoding.EncodeToString(buf)
		codes = append(codes, raw[0:4]+"-"+raw[4:8]+"-"+raw[8:12]+"-"+raw[12:16])
		hashes = append(hashes, domain.HashToken(raw))
	}
	return codes, hashes, nil
}

// normalizeRecoveryCode aceita o código como a pessoa o digitar: com ou
// sem hífens e espaços, em qualquer caixa.
func normalizeRecoveryCode(code string) string {
	r := strings.NewReplacer("-", "", " ", "")
	return strings.ToLower(r.Replace(strings.TrimSpace(code)))
}
