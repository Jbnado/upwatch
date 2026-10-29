package probe_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
	"github.com/Jbnado/upwatch/internal/testdir"
)

const uiToken = "token-fixo-de-teste-da-interface-0123456789"

type fakeBackend struct {
	path    string
	reloads atomic.Int32
}

func (f *fakeBackend) Config() probe.Config {
	cfg, _ := probe.Load(f.path)
	return cfg
}

func (f *fakeBackend) State() probe.Status {
	return probe.Status{Checks: map[string]*probe.CheckStatus{
		"rq-workers": {Result: &probe.ResultStatus{At: epoch, Status: "down", Msg: "SCARD rq:workers:default = 0, esperado >= 1"}},
	}}
}

func (f *fakeBackend) Reload() { f.reloads.Add(1) }

func uiServer(t *testing.T, token string) (*httptest.Server, *fakeBackend) {
	t.Helper()
	path := filepath.Join(testdir.New(t), "probe.yaml")
	cfg := probe.Config{Name: "vm-1", UIToken: token, Checks: []probe.Check{redisCheck("rq-workers")}}
	if err := probe.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{path: path}
	srv := httptest.NewServer(probe.NewUI(path, b))
	t.Cleanup(srv.Close)
	return srv, b
}

func call(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Escutar só em 127.0.0.1 não basta: qualquer processo da VM, ou um
// contêiner na mesma rede, alcança a porta. O token é exigido sempre.
func TestUIRequiresTheToken(t *testing.T) {
	srv, _ := uiServer(t, uiToken)
	for _, tc := range []struct{ method, path, token string }{
		{http.MethodGet, "/api/checks", ""},
		{http.MethodGet, "/api/checks", "errado"},
		{http.MethodGet, "/api/checks", uiToken + "x"},
		{http.MethodPost, "/api/checks", ""},
		{http.MethodDelete, "/api/checks/rq-workers", ""},
	} {
		if resp := call(t, tc.method, srv.URL+tc.path, tc.token, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with token %q = %d, want 401", tc.method, tc.path, tc.token, resp.StatusCode)
		}
	}
	if resp := call(t, http.MethodGet, srv.URL+"/api/checks", uiToken, ""); resp.StatusCode != http.StatusOK {
		t.Errorf("GET with the right token = %d, want 200", resp.StatusCode)
	}
}

// Sem token configurado a interface fica fechada: "sem token" não pode
// virar "sem senha".
func TestUIWithoutConfiguredTokenStaysClosed(t *testing.T) {
	srv, _ := uiServer(t, "")
	for _, token := range []string{"", " "} {
		if resp := call(t, http.MethodGet, srv.URL+"/api/checks", token, ""); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET with token %q and none configured = %d, want 401", token, resp.StatusCode)
		}
	}
}

// A listagem mostra estado e causa, mas nunca o token do push nem a
// senha: a interface pode estar aberta numa tela compartilhada.
func TestUIListShowsStateWithoutSecrets(t *testing.T) {
	srv, _ := uiServer(t, uiToken)
	resp := call(t, http.MethodGet, srv.URL+"/api/checks", uiToken, "")
	raw, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(raw), "token-da-verificacao-0001") || strings.Contains(string(raw), uiToken) {
		t.Fatalf("list shows a secret: %s", raw)
	}
	var got struct {
		Checks []struct {
			Name   string `json:"name"`
			Result *struct {
				Status string `json:"status"`
				Msg    string `json:"msg"`
			} `json:"result"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("list is not JSON: %v (%s)", err, raw)
	}
	if len(got.Checks) != 1 || got.Checks[0].Result == nil || got.Checks[0].Result.Status != "down" {
		t.Errorf("list = %s", raw)
	}
}

func TestUIAddsAndRemovesChecks(t *testing.T) {
	srv, b := uiServer(t, uiToken)

	body := `{"name":"bff","type":"http","target":"http://bff-api:3000/health","push_url":"` + checkURL +
		`","interval":"30s","expect_status":[200]}`
	if resp := call(t, http.MethodPost, srv.URL+"/api/checks", uiToken, body); resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST = %d %s", resp.StatusCode, raw)
	}
	cfg, err := probe.Load(b.path)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.Find("bff")
	if !ok || time.Duration(c.Interval) != 30*time.Second {
		t.Fatalf("after POST config = %+v", cfg.Checks)
	}
	// A mudança vale na hora, sem esperar a próxima conferência do arquivo.
	if b.reloads.Load() == 0 {
		t.Error("POST did not ask the daemon to reload")
	}

	if resp := call(t, http.MethodDelete, srv.URL+"/api/checks/rq-workers", uiToken, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	cfg, _ = probe.Load(b.path)
	if _, ok := cfg.Find("rq-workers"); ok {
		t.Error("DELETE kept the check")
	}
}

func TestUIRejectsAnInvalidCheck(t *testing.T) {
	srv, b := uiServer(t, uiToken)
	body := `{"name":"fila","type":"redis","target":"redis:6379","push_url":"` + checkURL + `","assert":["FLUSHALL > 0"]}`
	resp := call(t, http.MethodPost, srv.URL+"/api/checks", uiToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST with a write command = %d, want 400", resp.StatusCode)
	}
	raw, _ := os.ReadFile(b.path)
	if strings.Contains(string(raw), "fila") {
		t.Error("the rejected check reached the file")
	}
}

// A página em si não tem dado nenhum; o que importa é o cabeçalho que a
// impede de rodar script de fora ou ser embutida noutro site.
func TestUIPageIsServedWithAStrictPolicy(t *testing.T) {
	srv, _ := uiServer(t, uiToken)
	resp := call(t, http.MethodGet, srv.URL+"/", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "upwatch-probe") {
		t.Errorf("page does not look like the probe UI")
	}
}

// Fora do loopback a interface só escuta com a flag explícita: abrir a
// porta para a rede é uma decisão, não um padrão.
func TestValidateUIListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"} {
		if err := probe.ValidateUIListen(addr, false); err != nil {
			t.Errorf("ValidateUIListen(%q) = %v, want loopback accepted", addr, err)
		}
	}
	for _, addr := range []string{":8090", "0.0.0.0:8090", "10.0.0.5:8090", "[::]:8090", "8090"} {
		if err := probe.ValidateUIListen(addr, false); err == nil {
			t.Errorf("ValidateUIListen(%q) accepted a non-loopback address without --ui-listen-any", addr)
		}
	}
	if err := probe.ValidateUIListen("0.0.0.0:8090", true); err != nil {
		t.Errorf("ValidateUIListen with listen-any = %v", err)
	}
}
