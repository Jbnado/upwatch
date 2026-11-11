package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// tokensDe conta os tokens de API que a conta ainda tem.
func tokensDe(t *testing.T, f *fixture, userID int64) int {
	t.Helper()

	tokens, err := f.store.Tokens().List(context.Background(), userID)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	return len(tokens)
}

// emiteToken emite um token de API para a conta.
func emiteToken(t *testing.T, f *fixture, userID int64, nome string) {
	t.Helper()

	if _, _, err := f.svc.IssueToken(context.Background(), userID, nome, nil); err != nil {
		t.Fatalf("IssueToken returned unexpected error: %v", err)
	}
}

// outraConta cria uma segunda conta, para servir de ator do reset e de
// controle do que a revogação em massa não pode alcançar.
func outraConta(t *testing.T, f *fixture, nome string) domain.User {
	t.Helper()

	u, err := f.svc.CreateUser(context.Background(), nome, goodPassword, domain.RoleAdmin, domain.Scope{})
	if err != nil {
		t.Fatalf("CreateUser returned unexpected error: %v", err)
	}
	return u
}

// Desligar o próprio segundo fator derruba as outras sessões, e tem de
// derrubar os tokens pelo mesmo motivo: é operação de quem está arrumando
// a casa depois de uma suspeita.
func TestDisablingTOTPRevokesTheAPITokens(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	emiteToken(t, f, e.user.ID, "ci")

	if err := f.svc.DisableTOTP(context.Background(), e.user.ID, auth.Credential{}, goodPassword, f.code(t, e.secret)); err != nil {
		t.Fatalf("DisableTOTP returned unexpected error: %v", err)
	}

	if n := tokensDe(t, f, e.user.ID); n != 0 {
		t.Errorf("sobraram %d tokens depois de desligar o segundo fator", n)
	}
}

// O administrador que desliga o segundo fator de outra conta está
// respondendo a "perdi o celular" — ou a um incidente. Nos dois casos a
// credencial programática da conta não pode atravessar o reset: ela é
// exatamente a que o segundo fator não cobre.
func TestResettingAnotherAccountsTOTPRevokesItsAPITokens(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	ator := outraConta(t, f, "operadora")
	emiteToken(t, f, e.user.ID, "ci-da-conta")
	emiteToken(t, f, ator.ID, "ci-de-quem-reseta")

	if err := f.svc.ResetTOTP(context.Background(), ator.ID, e.user.ID); err != nil {
		t.Fatalf("ResetTOTP returned unexpected error: %v", err)
	}

	if n := tokensDe(t, f, e.user.ID); n != 0 {
		t.Errorf("sobraram %d tokens da conta resetada", n)
	}
	// E só os dela: revogar em massa não pode alcançar quem não tem nada a
	// ver com o reset, senão responder a um incidente derrubaria a
	// automação da instalação inteira.
	if n := tokensDe(t, f, ator.ID); n != 1 {
		t.Errorf("tokens de quem resetou = %d, queria 1 intacto", n)
	}
}

// A saída de emergência pela linha de comando (upwatch disable-2fa) é a
// ação mais drástica do sistema: desliga o fator sem senha, sem código e
// sem sessão. Deixar o token de API de pé ali seria deixar justamente a
// credencial que o fator nunca cobriu.
func TestResettingTOTPByUsernameRevokesTheAPITokens(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	emiteToken(t, f, e.user.ID, "ci")

	if err := f.svc.ResetTOTPByUsername(context.Background(), e.user.Username); err != nil {
		t.Fatalf("ResetTOTPByUsername returned unexpected error: %v", err)
	}

	if n := tokensDe(t, f, e.user.ID); n != 0 {
		t.Errorf("sobraram %d tokens depois da saída pela linha de comando", n)
	}
}

// ---------- quando a revogação falha ----------

// storeComRevogaçãoQuebrada faz a revogação de tokens falhar, para os
// testes poderem olhar o que sobra.
//
// A suíte não tinha nenhuma injeção de falha de store, e por isso "o
// chamador recebe o erro" era afirmação não verificada: trocar o retorno
// da revogação por `_ = err; return nil` passava tudo.
type storeComRevogaçãoQuebrada struct {
	store.Store
	erro error
}

func (s storeComRevogaçãoQuebrada) Tokens() store.TokenRepo {
	return tokenRepoQuebrado{TokenRepo: s.Store.Tokens(), erro: s.erro}
}

type tokenRepoQuebrado struct {
	store.TokenRepo
	erro error
}

func (r tokenRepoQuebrado) RevokeAllByUser(context.Context, int64, string, *int64) (int64, error) {
	return 0, r.erro
}

func (r tokenRepoQuebrado) RevokeOthers(context.Context, int64, string, string, *int64) error {
	return r.erro
}

// Revogação que falha não pode deixar o estado mudado, senão o retry é uma
// armadilha: o operador recebe erro, repete, e a segunda tentativa recusa
// porque o fator já está desligado — concluindo que está feito, com o
// token vivo. O token não vence e não passa pelo segundo fator, então
// "vivo" aqui é para sempre.
func TestAFailedRevocationLeavesTheStateUnchanged(t *testing.T) {
	f := newFixture(t)
	e := f.withTOTP(t)
	emiteToken(t, f, e.user.ID, "ci")

	quebrado := auth.New(
		storeComRevogaçãoQuebrada{Store: f.store, erro: errors.New("banco fora do ar")},
		auth.Options{Clock: f.clock},
	)
	if err := quebrado.ResetTOTPByUsername(context.Background(), e.user.Username); err == nil {
		t.Fatal("ResetTOTPByUsername escondeu a falha da revogação")
	}

	u, err := f.store.Users().Get(context.Background(), e.user.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if !u.TOTPEnabled {
		t.Error("o fator foi desligado apesar de a revogação ter falhado: repetir vai recusar")
	}

	// E repetir, com o banco de volta, termina o serviço.
	if err := f.svc.ResetTOTPByUsername(context.Background(), e.user.Username); err != nil {
		t.Fatalf("a segunda tentativa falhou: %v", err)
	}
	if n := tokensDe(t, f, e.user.ID); n != 0 {
		t.Errorf("sobraram %d tokens depois do retry", n)
	}
}

// Mesma coisa na troca de senha: se a revogação falha, a senha não mudou,
// e repetir funciona com a senha antiga. Na ordem inversa o operador
// recebe erro com a senha nova já em vigor, e a tentativa seguinte é
// recusada por "senha atual errada" — queimando tentativas do bloqueio.
func TestAFailedRevocationLeavesThePasswordUnchanged(t *testing.T) {
	f := newFixture(t)
	u := f.withAdmin(t)
	emiteToken(t, f, u.ID, "ci")

	quebrado := auth.New(
		storeComRevogaçãoQuebrada{Store: f.store, erro: errors.New("banco fora do ar")},
		auth.Options{Clock: f.clock},
	)
	const nova = "outra-senha-bem-longa"
	if err := quebrado.ChangePassword(context.Background(), u.ID, goodPassword, nova); err == nil {
		t.Fatal("ChangePassword escondeu a falha da revogação")
	}

	atual, err := f.store.Users().Get(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if !atual.CheckPassword(goodPassword) {
		t.Error("a senha mudou apesar de a revogação ter falhado: o retry vai recusar a senha antiga")
	}
}
