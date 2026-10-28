package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

// O json aceita "Password" no lugar de "password", e o checker usa a
// senha assim enviada. Só a grafia canônica pode chegar ao banco: é ela
// que a ocultação e a preservação conhecem, e uma grafia alternativa
// gravada como veio passaria inteira pelo GET.
func TestMonitorPasswordInAnotherSpellingNeverLeavesTheAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.createMonitor(t, redisMonitorBody("cache:6379", map[string]any{"Password": monitorSecret}))

	r := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, r, http.StatusOK)
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), monitorSecret) {
		t.Errorf("GET leaks the password sent as Password: %s", body)
	}
	stored := s.storedPassword(t, id)
	if !strings.Contains(stored, `"password":"`+monitorSecret+`"`) || strings.Contains(stored, `"Password"`) {
		t.Errorf("stored config = %s, want only the canonical password field", stored)
	}
}

const (
	basicAuthSecret = "senha-do-basic-auth"
	bearerSecret    = "tok-do-bearer-de-producao"
	apiKeySecret    = "chave-da-api-de-producao"
)

func httpMonitorBody(target string) map[string]any {
	return map[string]any{
		"name": "api", "type": "http", "target": target,
		"interval_seconds": 60, "timeout_seconds": 10,
		"config": map[string]any{
			"basic_auth_user": "monitor",
			"basic_auth_pass": basicAuthSecret,
			"headers": map[string]string{
				"Authorization": "Bearer " + bearerSecret,
				"X-Api-Key":     apiKeySecret,
				"Accept":        "application/json",
			},
		},
	}
}

func leaksHTTPSecret(body string) string {
	for _, secret := range []string{basicAuthSecret, bearerSecret, apiKeySecret} {
		if strings.Contains(body, secret) {
			return secret
		}
	}
	return ""
}

// Issue #54: a senha do basic auth e os cabeçalhos de credencial do
// monitor HTTP voltavam na criação, no GET, na listagem e no fluxo de
// eventos, para qualquer um com acesso de leitura ao painel. Toda saída
// do monitor passa pelo teste, e a credencial continua guardada.
func TestHTTPMonitorCredentialsNeverLeaveTheAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.AddCookie(s.cookie)
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream returned unexpected error: %v", err)
	}
	defer stream.Body.Close()

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", httpMonitorBody("https://api.exemplo.com/saude"))
	assertStatus(t, resp, http.StatusCreated)
	created, _ := io.ReadAll(resp.Body)
	if leak := leaksHTTPSecret(string(created)); leak != "" {
		t.Errorf("create response leaks %q: %s", leak, created)
	}
	var m map[string]any
	if err := json.Unmarshal(created, &m); err != nil {
		t.Fatalf("decoding the create response: %v", err)
	}
	id := int64(m["id"].(float64))

	for name, path := range map[string]string{
		"GET":      "/api/v1/monitors/" + itoa(id),
		"listagem": "/api/v1/monitors",
	} {
		r := s.do(t, http.MethodGet, path, nil)
		assertStatus(t, r, http.StatusOK)
		body, _ := io.ReadAll(r.Body)
		if leak := leaksHTTPSecret(string(body)); leak != "" {
			t.Errorf("%s leaks %q: %s", name, leak, body)
		}
	}

	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, `"id":`+itoa(id)) {
			continue
		}
		if leak := leaksHTTPSecret(line); leak != "" {
			t.Errorf("event stream leaks %q: %s", leak, line)
		}
		break
	}

	stored := s.storedPassword(t, id)
	for _, secret := range []string{basicAuthSecret, bearerSecret, apiKeySecret} {
		if !strings.Contains(stored, secret) {
			t.Errorf("stored config lost %q: %s", secret, stored)
		}
	}
}

// O formulário reenvia a configuração que recebeu do GET: sem a senha do
// basic auth e com os cabeçalhos mascarados. Salvar a edição de outro
// campo mantém as credenciais guardadas, e não grava a máscara.
func TestUpdateHTTPMonitorKeepsTheCredentials(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, httpMonitorBody("https://api.exemplo.com/saude"))

	r := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, r, http.StatusOK)
	got := decode[map[string]any](t, r)
	body := httpMonitorBody("https://api.exemplo.com/saude")
	body["config"] = got["config"]
	body["interval_seconds"] = 120

	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), body)
	assertStatus(t, resp, http.StatusOK)

	stored := s.storedPassword(t, id)
	for _, secret := range []string{basicAuthSecret, bearerSecret, apiKeySecret} {
		if !strings.Contains(stored, secret) {
			t.Errorf("stored config lost %q: %s", secret, stored)
		}
	}
	if strings.Contains(stored, "***") || strings.Contains(stored, "basic_auth_pass_set") {
		t.Errorf("the output-only mask or marker was stored: %s", stored)
	}
}

// A configuração sai por um caminho só, com uma regra por público: o
// administrador lê de volta o token do push, que ele copia para a rotina,
// e nada mais; o observador não lê segredo nenhum. As senhas dos
// protocolos e do basic auth e os cabeçalhos de credencial não voltam
// para ninguém. Dois caminhos de ocultação, um por PR, divergiriam: o
// primeiro tipo novo com segredo seria lembrado num e esquecido no outro.
func TestConfigSecretsFollowTheAudience(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	ids := []int64{
		s.createMonitor(t, redisMonitorBody("cache:6379", map[string]any{"password": monitorSecret})),
		s.createMonitor(t, httpMonitorBody("https://api.exemplo.com/saude")),
		s.createMonitor(t, pushMonitorBody("backup", pushToken)),
	}
	pushID := ids[2]

	readAll := func(t *testing.T) map[string]string {
		t.Helper()
		bodies := map[string]string{}
		paths := []string{"/api/v1/monitors"}
		for _, id := range ids {
			paths = append(paths, "/api/v1/monitors/"+itoa(id))
		}
		for _, path := range paths {
			r := s.do(t, http.MethodGet, path, nil)
			assertStatus(t, r, http.StatusOK)
			body, _ := io.ReadAll(r.Body)
			bodies[path] = string(body)
		}
		return bodies
	}

	for path, body := range readAll(t) {
		if strings.Contains(body, monitorSecret) {
			t.Errorf("admin %s leaks the redis password: %s", path, body)
		}
		if leak := leaksHTTPSecret(body); leak != "" {
			t.Errorf("admin %s leaks %q: %s", path, leak, body)
		}
	}
	// Controle positivo: sem ele, a verificação do observador abaixo
	// passaria também se o token não voltasse para ninguém.
	if body := readAll(t)["/api/v1/monitors/"+itoa(pushID)]; !strings.Contains(body, pushToken) {
		t.Fatalf("the admin does not see the push token: %s", body)
	}

	s.comoObservador(t)
	for path, body := range readAll(t) {
		if strings.Contains(body, monitorSecret) || strings.Contains(body, pushToken) {
			t.Errorf("viewer %s leaks a secret: %s", path, body)
		}
		if leak := leaksHTTPSecret(body); leak != "" {
			t.Errorf("viewer %s leaks %q: %s", path, leak, body)
		}
	}
}
