package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// Contas e papéis.
//
// O que se verifica aqui é sobretudo o que um observador NÃO consegue
// fazer. Uma permissão que só existe na interface — botão escondido,
// tela que não aparece — é permissão nenhuma: qualquer pessoa com o
// token faz a mesma chamada por curl.

// criarConta cadastra uma conta e devolve o id.
func (s *server) criarConta(t *testing.T, usuario, papel string) int64 {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": usuario, "password": "senha-de-teste-12345", "role": papel,
	})
	assertStatus(t, resp, http.StatusCreated)
	return decode[idResp](t, resp).ID
}

// comoObservador troca a sessão pela de uma conta só de leitura.
func (s *server) comoObservador(t *testing.T) {
	t.Helper()

	s.criarConta(t, "observador", "viewer")

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "observador", "password": "senha-de-teste-12345",
	})
	assertStatus(t, resp, http.StatusOK)

	for _, c := range resp.Cookies() {
		if c.Name == "upwatch_session" {
			s.cookie = c
			return
		}
	}
	t.Fatal("login do observador não devolveu cookie")
}

func TestSetupCreatesAnAdmin(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/auth/me", nil)
	assertStatus(t, resp, http.StatusOK)

	// A primeira conta precisa administrar: é ela que vai criar as
	// demais, e uma instalação que nasce sem administrador não teria como
	// sair desse estado.
	if !strings.Contains(readBody(t, resp), `"role":"admin"`) {
		t.Error("a conta inicial não é administradora")
	}
}

func TestViewerCannotWrite(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.comoObservador(t)

	// Cada um destes altera o estado do sistema. Um observador que
	// consiga qualquer um deles não é observador.
	casos := []struct {
		metodo, rota string
		corpo        map[string]any
	}{
		{http.MethodPost, "/api/v1/monitors", map[string]any{
			"name": "novo", "type": "http", "target": "https://exemplo.com",
			"interval_seconds": 60, "timeout_seconds": 10,
		}},
		{http.MethodPost, "/api/v1/channels", map[string]any{
			"name": "canal", "type": "webhook", "config": map[string]any{"url": "https://exemplo.com"},
		}},
		{http.MethodPost, "/api/v1/status-pages", map[string]any{"slug": "x", "title": "X"}},
		{http.MethodPost, "/api/v1/announcements", map[string]any{
			"title": "Falha", "impact": "major", "phase": "investigating", "global": true,
		}},
		{http.MethodPost, "/api/v1/users", map[string]any{
			"username": "outro", "password": "senha-de-teste-12345", "role": "admin",
		}},
		{http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "meu"}},
	}

	for _, caso := range casos {
		t.Run(caso.metodo+" "+caso.rota, func(t *testing.T) {
			resp := s.do(t, caso.metodo, caso.rota, caso.corpo)

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("observador conseguiu %s %s: status %d — %s",
					caso.metodo, caso.rota, resp.StatusCode, readBody(t, resp))
			}
		})
	}
}

func TestViewerCanRead(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "api", "type": "http", "target": "https://exemplo.com",
		"interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)

	s.comoObservador(t)

	// Ler é o ponto de existir o papel: acompanhar sem poder mexer.
	for _, rota := range []string{
		"/api/v1/monitors", "/api/v1/channels", "/api/v1/incidents",
		"/api/v1/status-pages", "/api/v1/announcements", "/api/v1/auth/me",
	} {
		t.Run(rota, func(t *testing.T) {
			assertStatus(t, s.do(t, http.MethodGet, rota, nil), http.StatusOK)
		})
	}
}

func TestViewerCannotListUsers(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.comoObservador(t)

	// A lista de contas é material de reconhecimento: nomes de usuário
	// válidos são metade do trabalho de quem tenta entrar.
	resp := s.do(t, http.MethodGet, "/api/v1/users", nil)

	assertStatus(t, resp, http.StatusForbidden)
}

func TestViewerCanChangeOwnPassword(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.comoObservador(t)

	// Trocar a própria senha não é administrar. Impedir isso obrigaria a
	// pedir a um administrador toda vez que alguém suspeitasse da
	// própria credencial — o momento em que a troca mais importa.
	resp := s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "senha-de-teste-12345",
		"new_password":     "outra-senha-de-teste-12345",
	})

	assertStatus(t, resp, http.StatusNoContent)
}

func TestLastAdminCannotBeRemoved(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	me := decode[idResp](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))

	// Remover o último administrador deixaria a instalação sem ninguém
	// capaz de criar outro, e a única saída seria mexer no banco à mão.
	resp := s.do(t, http.MethodDelete, "/api/v1/users/"+itoa(me.ID), nil)

	assertStatus(t, resp, http.StatusConflict)
}

func TestLastAdminCannotBeDemoted(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	me := decode[idResp](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))

	resp := s.do(t, http.MethodPut, "/api/v1/users/"+itoa(me.ID),
		map[string]any{"role": "viewer"})

	assertStatus(t, resp, http.StatusConflict)
}

func TestAdminCanBeRemovedWhenAnotherRemains(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	segundo := s.criarConta(t, "outro-admin", "admin")

	// Com dois administradores, remover um é operação normal — a guarda
	// protege o último, não a operação.
	resp := s.do(t, http.MethodDelete, "/api/v1/users/"+itoa(segundo), nil)

	assertStatus(t, resp, http.StatusNoContent)
}

func TestDeletingUserEndsTheirSession(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	admin := s.cookie
	s.comoObservador(t)
	observador := s.cookie

	// Volta para o administrador e remove a conta do observador.
	s.cookie = admin
	lista := decode[struct {
		Items []struct {
			ID   int64  `json:"id"`
			Role string `json:"role"`
		} `json:"items"`
	}](t, s.do(t, http.MethodGet, "/api/v1/users", nil))

	var alvo int64
	for _, u := range lista.Items {
		if u.Role == "viewer" {
			alvo = u.ID
		}
	}
	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/users/"+itoa(alvo), nil), http.StatusNoContent)

	// Quem perdeu o acesso não pode continuar dentro até o cookie vencer.
	s.cookie = observador
	resp := s.do(t, http.MethodGet, "/api/v1/monitors", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestCreatingUserRejectsUnknownRole(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "ana", "password": "senha-de-teste-12345", "role": "superadmin",
	})

	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"role"`) {
		t.Error("erro não apontou o campo role")
	}
}

func TestCreatingUserRejectsDuplicateUsername(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	s.criarConta(t, "ana", "viewer")

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "ana", "password": "senha-de-teste-12345", "role": "viewer",
	})

	assertStatus(t, resp, http.StatusConflict)
}

func TestUserListNeverLeaksHashes(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.criarConta(t, "ana", "viewer")

	corpo := readBody(t, s.do(t, http.MethodGet, "/api/v1/users", nil))

	// A tela de contas mostra quem existe, não material para ataque
	// offline.
	for _, agulha := range []string{"password_hash", "$2a$", "$2b$"} {
		if strings.Contains(corpo, agulha) {
			t.Errorf("listagem de contas vazou %q:\n%s", agulha, corpo)
		}
	}
}
