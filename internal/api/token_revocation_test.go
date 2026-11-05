package api_test

import (
	"net/http"
	"testing"
)

// criaToken emite um token de API e devolve o segredo, que só existe na
// resposta de criação.
func criaToken(t *testing.T, s *server, nome string) string {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": nome})
	assertStatus(t, resp, http.StatusCreated)
	segredo, _ := decode[map[string]any](t, resp)["token"].(string)
	if segredo == "" {
		t.Fatal("a resposta de criação não trouxe o segredo do token")
	}
	return segredo
}

// exigeToken confere o que a credencial programática consegue agora.
func exigeToken(t *testing.T, s *server, segredo string, querido int) {
	t.Helper()
	assertStatus(t, s.withToken(t, segredo, http.MethodGet, "/api/v1/monitors", nil), querido)
}

// Trocar a senha porque ela pode ter vazado não adianta se a credencial
// que quem a obteve emitiu continuar valendo. As sessões já caíam — é o
// propósito declarado da troca —, mas o token de API sobrevivia, e ele faz
// tudo o que a interface faz, não vence por padrão e não passa pelo
// segundo fator. Era a credencial mais poderosa das duas a sobreviver.
func TestChangingThePasswordRevokesTheAPITokens(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	segredo := criaToken(t, s, "ci")
	exigeToken(t, s, segredo, http.StatusOK)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": adminPassword, "new_password": "uma-senha-nova-bem-longa",
	}), http.StatusNoContent)

	exigeToken(t, s, segredo, http.StatusUnauthorized)
}

// Ligar o segundo fator é a resposta de quem desconfia da própria senha, e
// o comentário do ConfirmTOTP diz que a sessão de quem a roubou não pode
// sobreviver à proteção nova. O token que essa pessoa emitiu sobrevivia —
// e justamente ele não passa pelo segundo fator, então a proteção nova não
// o alcançava nem depois.
//
// Irmão de TestEnablingTOTPRevokesOtherSessionsViaAPI, que cobre o outro
// tipo de credencial.
func TestEnablingTOTPRevokesTheAPITokensViaAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	segredo := criaToken(t, s, "ci")
	exigeToken(t, s, segredo, http.StatusOK)

	s.enroll(t)

	exigeToken(t, s, segredo, http.StatusUnauthorized)
	// A sessão de quem está ligando continua: é ela que acabou de ligar.
	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusOK)
}
