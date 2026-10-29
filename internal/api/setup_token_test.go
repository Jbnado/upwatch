package api_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
)

// A configuração inicial não tem login: quem chega primeiro numa instância
// recém-publicada criaria a conta de administração e tomaria a instalação.
// O token, que só quem tem acesso ao servidor lê (no log ou na variável),
// é o que separa o dono de um visitante qualquer.
func TestSetupRequiresTheSetupToken(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"sem token":    {"username": "intruso", "password": adminPassword},
		"token errado": {"username": "intruso", "password": adminPassword, "setup_token": "chute"},
		"token vazio":  {"username": "intruso", "password": adminPassword, "setup_token": ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)

			resp := s.do(t, http.MethodPost, "/api/v1/setup", body)

			assertStatus(t, resp, http.StatusUnauthorized)
			got := decode[map[string]map[string]string](t, resp)
			if got["error"]["field"] != "setup_token" {
				t.Errorf("error field = %q, want setup_token, so the screen points at the right input", got["error"]["field"])
			}
			// A recusa não pode ter criado a conta: o assistente segue
			// aberto para o dono, que tem o token.
			need, err := s.auth.NeedsSetup(t.Context())
			if err != nil {
				t.Fatalf("NeedsSetup returned unexpected error: %v", err)
			}
			if !need {
				t.Error("a refused setup created the account")
			}
		})
	}
}

// Sem token configurado o assistente fica fechado, e não aberto: quem monta
// a API sem passar o token esqueceu um passo, e o esquecimento não pode
// devolver a instância a quem chegar primeiro.
func TestSetupWithoutConfiguredTokenStaysClosed(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.SetupToken = "" })

	for _, token := range []string{"", "qualquer-coisa"} {
		resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
			"username": "admin", "password": adminPassword, "setup_token": token,
		})
		assertStatus(t, resp, http.StatusUnauthorized)
	}
}

// Depois que a conta existe o token deixa de valer: a resposta é a mesma
// de antes do token existir, e uma instância já configurada não muda de
// comportamento com a atualização.
func TestSetupTokenStopsWorkingOnceTheAccountExists(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "segunda", "password": adminPassword, "setup_token": testSetupToken,
	})

	assertStatus(t, resp, http.StatusConflict)
}

// Uma instância configurada antes do token existir não tem token nenhum
// (nada é gerado quando a conta já existe). O cliente antigo, que não
// manda o campo, tem de continuar recebendo o 409 de sempre, e não um 401
// que sugeriria haver algo a destravar.
func TestSetupOnConfiguredInstanceWithoutTokenAnswersAsBefore(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.SetupToken = "" })
	if _, err := s.auth.CreateInitialAdmin(t.Context(), "admin", adminPassword); err != nil {
		t.Fatalf("CreateInitialAdmin returned unexpected error: %v", err)
	}

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "intruso", "password": adminPassword,
	})

	assertStatus(t, resp, http.StatusConflict)
}

// O token gerado tem entropia de sobra para chute, mas um token escolhido
// à mão pode não ter. O limite por origem torna a adivinhação inviável
// sem depender da qualidade do valor, e não atrapalha quem erra a cópia
// algumas vezes.
func TestSetupLimitsAttemptsPerOrigin(t *testing.T) {
	s := newServer(t)

	chute := map[string]any{"username": "intruso", "password": adminPassword, "setup_token": "chute"}

	var bloqueada *http.Response
	for i := 0; i < 100; i++ {
		resp := s.do(t, http.MethodPost, "/api/v1/setup", chute)
		if resp.StatusCode == http.StatusTooManyRequests {
			bloqueada = resp
			break
		}
		assertStatus(t, resp, http.StatusUnauthorized)
	}
	if bloqueada == nil {
		t.Fatal("100 wrong setup tokens from the same origin were never refused with 429")
	}
	if wait, err := strconv.Atoi(bloqueada.Header.Get("Retry-After")); err != nil || wait <= 0 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", bloqueada.Header.Get("Retry-After"))
	}

	// Mesmo com o token certo: senão o limite só valeria para quem erra, e
	// o atacante saberia que acertou pela resposta diferente.
	certo := map[string]any{"username": "admin", "password": adminPassword, "setup_token": testSetupToken}
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/setup", certo), http.StatusTooManyRequests)

	// Passado o tempo, o dono que errou a cópia consegue terminar.
	s.clock.Advance(time.Hour)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/setup", certo), http.StatusCreated)
}
