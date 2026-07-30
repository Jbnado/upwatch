package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/bernardojoao/upwatch/internal/api"
	"github.com/bernardojoao/upwatch/internal/auth"
	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

const adminPassword = "uma-senha-bem-longa"

// TestMain reduz o custo do bcrypt: o custo de produção existe para
// inviabilizar força bruta, e pagá-lo em cada caso só deixaria a suíte
// lenta o bastante para as pessoas pararem de rodá-la.
func TestMain(m *testing.M) {
	domain.PasswordHashCost = bcrypt.MinCost
	os.Exit(m.Run())
}

// server é a API sob teste, com store real e relógio controlado.
type server struct {
	*httptest.Server

	store *sqlstore.Store
	auth  *auth.Service
	clock *clock.Fake
	// cookie guarda a sessão obtida no login.
	cookie *http.Cookie
}

func newServer(t *testing.T) *server {
	t.Helper()

	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fake := clock.NewFake(epoch)
	authSvc := auth.New(st, auth.Options{Clock: fake})

	reg, err := checker.NewRegistry(
		checker.NewHTTP(), checker.NewTCP(), checker.NewDNS(),
		checker.NewTLS(), checker.NewICMP(), checker.NewPush(st, fake),
	)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}

	handler := api.New(api.Options{
		Store:    st,
		Auth:     authSvc,
		Checkers: reg,
		Clock:    fake,
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	return &server{Server: ts, store: st, auth: authSvc, clock: fake}
}

// do executa uma requisição, anexando a sessão quando houver.
func (s *server) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshalling body returned unexpected error: %v", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, payload)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cookie != nil {
		req.AddCookie(s.cookie)
	}

	// Sem seguir redirecionamento: a API não deve emitir nenhum, e segui-lo
	// esconderia o engano.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// withToken executa uma requisição autenticada por token de API.
func (s *server) withToken(t *testing.T, secret, method, path string, body any) *http.Response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, payload)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// setup cria a conta inicial e autentica a sessão do cliente.
func (s *server) setup(t *testing.T) {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup returned %d, want %d: %s", resp.StatusCode, http.StatusCreated, readBody(t, resp))
	}
	s.login(t)
}

func (s *server) login(t *testing.T) {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login returned %d, want 200: %s", resp.StatusCode, readBody(t, resp))
	}
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookieName {
			s.cookie = c
			return
		}
	}
	t.Fatal("login response carried no session cookie")
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body returned unexpected error: %v", err)
	}
	return string(b)
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding response returned unexpected error: %v", err)
	}
	return out
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, want, readBody(t, resp))
	}
}

// ---------- saúde e primeiro acesso ----------

// Liveness não pode exigir autenticação: orquestradores a consultam antes
// de qualquer credencial existir.
func TestHealthzNeedsNoAuthentication(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/healthz", nil)

	assertStatus(t, resp, http.StatusOK)
}

func TestSetupReportsWhetherItIsNeeded(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/api/v1/setup", nil)
	assertStatus(t, resp, http.StatusOK)

	got := decode[map[string]bool](t, resp)
	if !got["needs_setup"] {
		t.Error("needs_setup = false on a fresh install, want true")
	}
}

func TestSetupCreatesTheInitialAdmin(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "admin", "password": adminPassword,
	})

	assertStatus(t, resp, http.StatusCreated)
	body := readBody(t, resp)
	if bytes.Contains([]byte(body), []byte(adminPassword)) {
		t.Errorf("the response echoes the password: %s", body)
	}
}

// Reabrir o assistente permitiria a qualquer visitante criar uma conta e
// tomar a instalação.
func TestSetupRefusesOnceCompleted(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "intruso", "password": adminPassword,
	})

	assertStatus(t, resp, http.StatusConflict)
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "admin", "password": "curta",
	})

	assertStatus(t, resp, http.StatusBadRequest)
}

// ---------- autenticação ----------

func TestProtectedEndpointRejectsAnonymousRequest(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/monitors", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestLoginIssuesHardenedSessionCookie(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	// HttpOnly impede que script na página leia a sessão; SameSite fecha o
	// caminho mais comum de requisição forjada entre sites.
	if !cookie.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode && cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite = %v, want Lax or Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("session cookie Path = %q, want %q", cookie.Path, "/")
	}
}

func TestLoginWithWrongPasswordIsUnauthorized(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "senha-errada-porem-longa",
	})

	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestLogoutClearsTheSession(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/logout", nil)
	assertStatus(t, resp, http.StatusNoContent)

	resp = s.do(t, http.MethodGet, "/api/v1/monitors", nil)
	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestMeReturnsTheAuthenticatedUserWithoutSecrets(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/auth/me", nil)
	assertStatus(t, resp, http.StatusOK)

	body := readBody(t, resp)
	if !bytes.Contains([]byte(body), []byte(`"admin"`)) {
		t.Errorf("response omits the username: %s", body)
	}
	for _, secret := range []string{"password", "hash", adminPassword} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Errorf("response leaks %q: %s", secret, body)
		}
	}
}

// ---------- tokens de API ----------

func TestTokenAuthenticatesRequests(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	assertStatus(t, resp, http.StatusCreated)

	created := decode[map[string]any](t, resp)
	secret, _ := created["token"].(string)
	if secret == "" {
		t.Fatal("the creation response carries no token secret")
	}

	got := s.withToken(t, secret, http.MethodGet, "/api/v1/monitors", nil)
	assertStatus(t, got, http.StatusOK)
}

// O segredo só aparece na criação; depois disso nem o dono o recupera.
func TestTokenSecretIsNotListed(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	assertStatus(t, resp, http.StatusCreated)
	secret := decode[map[string]any](t, resp)["token"].(string)

	listing := s.do(t, http.MethodGet, "/api/v1/auth/tokens", nil)
	assertStatus(t, listing, http.StatusOK)

	body := readBody(t, listing)
	if bytes.Contains([]byte(body), []byte(secret)) {
		t.Errorf("the listing leaks the token secret: %s", body)
	}
	if !bytes.Contains([]byte(body), []byte(secret[:12])) {
		t.Errorf("the listing omits the prefix that identifies the token: %s", body)
	}
}

func TestInvalidTokenIsUnauthorized(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.withToken(t, "upw_inventado", http.MethodGet, "/api/v1/monitors", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestRevokedTokenStopsWorking(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	assertStatus(t, resp, http.StatusCreated)
	created := decode[map[string]any](t, resp)
	secret := created["token"].(string)
	id := int64(created["id"].(float64))

	del := s.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+itoa(id), nil)
	assertStatus(t, del, http.StatusNoContent)

	after := s.withToken(t, secret, http.MethodGet, "/api/v1/monitors", nil)
	assertStatus(t, after, http.StatusUnauthorized)
}

// ---------- respostas e cabeçalhos ----------

func TestErrorsUseAConsistentJSONShape(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/9999", nil)
	assertStatus(t, resp, http.StatusNotFound)

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	got := decode[map[string]any](t, resp)
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", got)
	}
	if errObj["message"] == "" || errObj["code"] == "" {
		t.Errorf("error object is missing code or message: %v", errObj)
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/healthz", nil)

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// Um defeito num handler não pode derrubar o processo e parar o
// monitoramento junto.
func TestPanicInHandlerBecomesServerError(t *testing.T) {
	s := newServer(t)

	resp := s.do(t, http.MethodGet, "/api/v1/_panic", nil)

	if resp.StatusCode != http.StatusInternalServerError && resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 500 from the recovery middleware or 404 if the route is absent",
			resp.StatusCode)
	}

	// O servidor continua atendendo.
	after := s.do(t, http.MethodGet, "/healthz", nil)
	assertStatus(t, after, http.StatusOK)
}

func TestMalformedJSONIsRejected(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		s.URL+"/api/v1/monitors", bytes.NewReader([]byte("{nao-e-json")))
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.cookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	defer resp.Body.Close()

	assertStatus(t, resp, http.StatusBadRequest)
}

func itoa(v int64) string {
	return json.Number(jsonInt(v)).String()
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// withHeader executa uma requisição anônima com um cabeçalho extra.
//
// Existe para exercitar a revalidação de cache da página pública, que é a
// única rota do UpWatch desenhada para receber a mesma consulta muitas
// vezes seguidas.
func (s *server) withHeader(t *testing.T, method, path, header, value string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, s.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set(header, value)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
