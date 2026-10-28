package api_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

const monitorSecret = "senha-do-redis-de-producao"

func redisMonitorBody(target string, config map[string]any) map[string]any {
	return map[string]any{
		"name":             "fila",
		"type":             "redis",
		"target":           target,
		"interval_seconds": 60,
		"timeout_seconds":  10,
		"config":           config,
	}
}

// storedPassword lê a senha direto do store, por fora da API.
func (s *server) storedPassword(t *testing.T, id int64) string {
	t.Helper()
	m, err := s.store.Monitors().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	return string(m.Config)
}

// A senha de um Redis ou banco monitorado é credencial de produção, e
// quem tem acesso de leitura ao painel não pode obtê-la — mesma regra da
// URL de um canal de aviso. Toda representação do monitor que sai pela
// API passa pelo teste: a resposta da criação, o GET e a listagem.
func TestMonitorPasswordNeverLeavesTheAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", redisMonitorBody("cache:6379",
		map[string]any{"password": monitorSecret, "db": 1}))
	assertStatus(t, resp, http.StatusCreated)
	created := decode[map[string]any](t, resp)
	id := int64(created["id"].(float64))

	for name, path := range map[string]string{
		"GET":      "/api/v1/monitors/" + itoa(id),
		"listagem": "/api/v1/monitors",
	} {
		r := s.do(t, http.MethodGet, path, nil)
		assertStatus(t, r, http.StatusOK)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), monitorSecret) {
			t.Errorf("%s leaks the password: %s", name, body)
		}
	}

	cfg, _ := created["config"].(map[string]any)
	if _, ok := cfg["password"]; ok {
		t.Errorf("create response carries the password: %v", cfg)
	}
	if cfg["password_set"] != true {
		t.Errorf("password_set = %v, want true so the form can say a password exists", cfg["password_set"])
	}

	// Omitir na saída não pode significar não guardar.
	if stored := s.storedPassword(t, id); !strings.Contains(stored, monitorSecret) {
		t.Errorf("stored config lost the password: %s", stored)
	}
}

// O formulário reenvia a configuração que recebeu, sem senha. Salvar a
// edição de outro campo não pode apagar a credencial.
func TestUpdateMonitorKeepsThePasswordWhenOmitted(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, redisMonitorBody("cache:6379", map[string]any{"password": monitorSecret}))

	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id),
		redisMonitorBody("cache:6379", map[string]any{"db": 3, "password_set": true}))
	assertStatus(t, resp, http.StatusOK)

	stored := s.storedPassword(t, id)
	if !strings.Contains(stored, monitorSecret) {
		t.Errorf("stored config lost the password: %s", stored)
	}
	if strings.Contains(stored, "password_set") {
		t.Errorf("the output-only marker was stored: %s", stored)
	}
}

// Trocar o endereço sem informar a senha levaria a credencial guardada a
// um servidor qualquer — o jeito de quem não pode lê-la obtê-la mesmo
// assim.
func TestUpdateMonitorRefusesToCarryThePasswordToAnotherTarget(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, redisMonitorBody("cache:6379", map[string]any{"password": monitorSecret}))

	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id),
		redisMonitorBody("servidor-alheio:6379", map[string]any{}))

	assertStatus(t, resp, http.StatusBadRequest)
	got := decode[map[string]map[string]any](t, resp)
	if got["error"]["field"] != "config" {
		t.Errorf("field = %v, want config", got["error"]["field"])
	}
	if stored := s.storedPassword(t, id); !strings.Contains(stored, monitorSecret) {
		t.Errorf("the refused update changed the stored config: %s", stored)
	}
}
