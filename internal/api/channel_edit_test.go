package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// criarCanal cadastra um canal e devolve o id.
func (s *server) criarCanal(t *testing.T, tipo string, config map[string]any) int64 {
	t.Helper()
	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "canal " + tipo, "type": tipo, "config": config,
	})
	assertStatus(t, resp, http.StatusCreated)
	return decode[idResp](t, resp).ID
}

// configGuardada lê a configuração direto do store, por fora da API.
func (s *server) configGuardada(t *testing.T, id int64) string {
	t.Helper()
	c, err := s.store.Channels().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	return string(c.Config)
}

func emailConfig() map[string]any {
	return map[string]any{
		"host": "smtp.exemplo.com", "username": "alertas",
		"password": "senha-do-smtp-secreta",
		"from":     "alertas@exemplo.com", "to": []string{"plantao@exemplo.com"},
	}
}

// O formulário de edição precisa dos valores atuais para não obrigar a
// redigitar tudo, e de saber que há uma senha para dizer "em branco,
// mantém" — sem recebê-la.
func TestChannelGetGivesTheEditorTheConfigWithoutSecrets(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.criarCanal(t, "email", emailConfig())

	resp := s.do(t, http.MethodGet, fmt.Sprintf("/api/v1/channels/%d", id), nil)
	assertStatus(t, resp, http.StatusOK)
	corpo := readBody(t, resp)

	if strings.Contains(corpo, "senha-do-smtp-secreta") {
		t.Errorf("GET leaks the SMTP password: %s", corpo)
	}
	for _, quer := range []string{`"host":"smtp.exemplo.com"`, `"password_set":true`, `"plantao@exemplo.com"`} {
		if !strings.Contains(corpo, quer) {
			t.Errorf("GET = %s, want it to contain %s", corpo, quer)
		}
	}
}

// Quem só lê não edita, e a configuração, mesmo sem segredo, tem
// destinatários e servidores que não são da conta dele.
func TestChannelGetGivesReadersNoConfig(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.criarCanal(t, "email", emailConfig())
	s.comoObservador(t)

	resp := s.do(t, http.MethodGet, fmt.Sprintf("/api/v1/channels/%d", id), nil)
	assertStatus(t, resp, http.StatusOK)
	if corpo := readBody(t, resp); strings.Contains(corpo, "config") || strings.Contains(corpo, "smtp.exemplo.com") {
		t.Errorf("GET as a viewer carries the config: %s", corpo)
	}
}

// Editar o canal sem redigitar a senha não pode apagá-la: o formulário
// não a recebe, então a edição chega sem ela.
func TestChannelUpdateKeepsTheSecretsThatDidNotComeBack(t *testing.T) {
	casos := []struct {
		tipo    string
		criar   map[string]any
		editar  map[string]any
		segredo string
	}{
		{"email", emailConfig(), map[string]any{
			"host": "smtp.exemplo.com", "username": "alertas",
			"from": "alertas@exemplo.com", "to": []string{"outro@exemplo.com"},
		}, "senha-do-smtp-secreta"},
		{"ntfy", map[string]any{
			"server": "https://ntfy.exemplo.com", "topic": "infra", "token": "tk_token-do-ntfy",
		}, map[string]any{
			"server": "https://ntfy.exemplo.com", "topic": "infra-prod",
		}, "tk_token-do-ntfy"},
		{"webhook", map[string]any{
			"url":     "https://hooks.exemplo.com/t/tok-webhook",
			"headers": map[string]string{"Authorization": "Bearer tok-cabecalho"},
		}, map[string]any{
			"headers":  map[string]string{"Authorization": "***"},
			"template": "{{.Monitor.Name}} mudou",
		}, "tok-cabecalho"},
	}

	for _, c := range casos {
		t.Run(c.tipo, func(t *testing.T) {
			s := newServer(t)
			s.setup(t)

			id := s.criarCanal(t, c.tipo, c.criar)
			resp := s.do(t, http.MethodPut, fmt.Sprintf("/api/v1/channels/%d", id), map[string]any{
				"name": "renomeado", "type": c.tipo, "config": c.editar,
			})
			assertStatus(t, resp, http.StatusOK)

			if guardada := s.configGuardada(t, id); !strings.Contains(guardada, c.segredo) {
				t.Errorf("the update dropped the stored secret: %s", guardada)
			}
		})
	}
}

func TestChannelUpdateWithoutAStoredWebhookURLIsRefused(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.criarCanal(t, "discord", map[string]any{"url": "https://discord.com/api/webhooks/1/tok"})

	// Trocar o tipo não leva a URL do Discord para o webhook genérico.
	resp := s.do(t, http.MethodPut, fmt.Sprintf("/api/v1/channels/%d", id), map[string]any{
		"name": "canal", "type": "webhook", "config": map[string]any{},
	})
	assertStatus(t, resp, http.StatusBadRequest)
}

// Editar é atualizar, não remover e recriar: os monitores e grupos que
// avisam pelo canal continuam avisando.
func TestChannelUpdateKeepsTheLinks(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	canal := s.criarCanal(t, "discord", map[string]any{"url": "https://discord.com/api/webhooks/1/tok"})
	monitor := s.createMonitor(t, newMonitorBody("api"))
	assertStatus(t, s.do(t, http.MethodPut, fmt.Sprintf("/api/v1/monitors/%d/channels/%d", monitor, canal), nil),
		http.StatusNoContent)

	resp := s.do(t, http.MethodPut, fmt.Sprintf("/api/v1/channels/%d", canal), map[string]any{
		"name": "renomeado", "type": "discord", "config": map[string]any{},
	})
	assertStatus(t, resp, http.StatusOK)

	resp = s.do(t, http.MethodGet, fmt.Sprintf("/api/v1/monitors/%d/channels", monitor), nil)
	assertStatus(t, resp, http.StatusOK)
	if corpo := readBody(t, resp); !strings.Contains(corpo, `"renomeado"`) {
		t.Errorf("the monitor lost the channel after the update: %s", corpo)
	}
}
