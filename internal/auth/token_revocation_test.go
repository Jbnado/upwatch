package auth_test

import (
	"context"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
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

	if err := f.svc.DisableTOTP(context.Background(), e.user.ID, "", goodPassword, f.code(t, e.secret)); err != nil {
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
