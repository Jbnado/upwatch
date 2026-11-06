package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/domain"
)

// browserRequest monta uma requisição como o navegador a mandaria: corpo
// cru, tipo de conteúdo à escolha e os cabeçalhos de origem dados.
type browserRequest struct {
	method, path string
	contentType  string
	body         string
	headers      map[string]string
	bearer       string
	noCookie     bool
}

func (s *server) send(t *testing.T, b browserRequest) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), b.method, s.URL+b.path,
		bytes.NewBufferString(b.body))
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	if b.contentType != "" {
		req.Header.Set("Content-Type", b.contentType)
	}
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	if b.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	if s.cookie != nil && !b.noCookie {
		req.AddCookie(s.cookie)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// assertErrorCode confere o código no formato padrão de erro.
func assertErrorCode(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	got := decode[map[string]map[string]any](t, resp)
	if code := got["error"]["code"]; code != want {
		t.Errorf("error code = %v, want %q", code, want)
	}
}

const newAdminBody = `{"username":"outro-admin","role":"admin","password":"uma-senha-bem-longa-2"}`

// userExists informa se a conta foi criada, para o teste provar que a
// recusa aconteceu antes de qualquer efeito.
func (s *server) userExists(t *testing.T, username string) bool {
	t.Helper()
	users, err := s.store.Users().List(context.Background())
	if err != nil {
		t.Fatalf("Users().List returned unexpected error: %v", err)
	}
	for _, u := range users {
		if u.Username == username {
			return true
		}
	}
	return false
}

// Corpo JSON declarado com outro tipo é recusado antes de qualquer efeito.
func TestJSONBodyWithoutJSONContentTypeIsRefused(t *testing.T) {
	cases := map[string]string{
		"text-plain": "text/plain",
		"form":       "application/x-www-form-urlencoded",
		"multipart":  "multipart/form-data; boundary=x",
		"absent":     "",
	}
	for name, ct := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.setup(t)

			resp := s.send(t, browserRequest{
				method: http.MethodPost, path: "/api/v1/users", contentType: ct, body: newAdminBody,
			})

			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want 415: %s", resp.StatusCode, readBody(t, resp))
			}
			assertErrorCode(t, resp, "unsupported_media_type")
			if s.userExists(t, "outro-admin") {
				t.Error("the account was created despite the refusal")
			}
		})
	}
}

func TestJSONContentTypeWithParametersIsAccepted(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/users",
		contentType: "application/json; charset=utf-8", body: newAdminBody,
	})

	assertStatus(t, resp, http.StatusCreated)
}

// Login e primeiro acesso não têm sessão, mas recebem a mesma exigência.
func TestLoginAndSetupRefuseNonJSONBody(t *testing.T) {
	s := newServer(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/setup", contentType: "text/plain",
		body: `{"username":"admin","password":"` + adminPassword + `","setup_token":"` + testSetupToken + `"}`,
	})
	assertStatus(t, resp, http.StatusUnsupportedMediaType)

	s.setup(t)
	resp = s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/auth/login", contentType: "text/plain", noCookie: true,
		body: `{"username":"admin","password":"` + adminPassword + `"}`,
	})
	assertStatus(t, resp, http.StatusUnsupportedMediaType)
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookieName {
			t.Error("a refused login still set the session cookie")
		}
	}
}

// Escrita pelo cookie só vale da própria origem.
func TestCookieWriteFromAnotherOriginIsRefused(t *testing.T) {
	cases := map[string]map[string]string{
		"sec-fetch-site cross-site": {"Sec-Fetch-Site": "cross-site"},
		"sec-fetch-site same-site":  {"Sec-Fetch-Site": "same-site"},
		"foreign origin":            {"Origin": "http://outro.exemplo"},
		"same host, other port":     {"Origin": "http://127.0.0.1:1"},
		"opaque origin":             {"Origin": "null"},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.setup(t)

			resp := s.send(t, browserRequest{
				method: http.MethodPost, path: "/api/v1/users",
				contentType: "application/json", body: newAdminBody, headers: headers,
			})

			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", resp.StatusCode, readBody(t, resp))
			}
			assertErrorCode(t, resp, "forbidden")
			if s.userExists(t, "outro-admin") {
				t.Error("the account was created despite the refusal")
			}
		})
	}
}

// Sem corpo também muda estado: apagar e sair passam pela mesma conferência.
func TestCookieDeleteFromAnotherOriginIsRefused(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/auth/logout",
		headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
	})
	assertStatus(t, resp, http.StatusForbidden)

	// A sessão continua valendo.
	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusOK)
}

func TestLoginFromAnotherSiteIsRefused(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/auth/login", contentType: "application/json",
		body: `{"username":"admin","password":"` + adminPassword + `"}`, noCookie: true,
		headers: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://outro.exemplo"},
	})

	assertStatus(t, resp, http.StatusForbidden)
}

// A própria interface, cliente sem navegador e o endereço digitado na barra
// passam.
func TestSameOriginAndNonBrowserWritesAreAccepted(t *testing.T) {
	cases := map[string]func(s *server) map[string]string{
		"no origin headers":          func(*server) map[string]string { return nil },
		"sec-fetch-site same-origin": func(*server) map[string]string { return map[string]string{"Sec-Fetch-Site": "same-origin"} },
		"sec-fetch-site none":        func(*server) map[string]string { return map[string]string{"Sec-Fetch-Site": "none"} },
		"origin matches host":        func(s *server) map[string]string { return map[string]string{"Origin": s.URL} },
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			s.setup(t)

			resp := s.send(t, browserRequest{
				method: http.MethodPost, path: "/api/v1/users",
				contentType: "application/json", body: newAdminBody, headers: headers(s),
			})

			assertStatus(t, resp, http.StatusCreated)
		})
	}
}

// Atrás de um proxy que reescreve o Host, o Origin traz o endereço externo.
// O endereço público configurado é o que o reconhece.
func TestPublicURLIsATrustedOrigin(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.PublicURL = "https://status.exemplo.com/" })
	s.setup(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/users", contentType: "application/json",
		body: newAdminBody, headers: map[string]string{"Origin": "https://status.exemplo.com"},
	})
	assertStatus(t, resp, http.StatusCreated)

	resp = s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/auth/logout",
		headers: map[string]string{"Origin": "https://outro.exemplo.com"},
	})
	assertStatus(t, resp, http.StatusForbidden)
}

// O token não vai sozinho numa requisição do navegador, então não há o que
// conferir; e a automação que roda numa página continua funcionando.
func TestBearerTokenIsNotSubjectToOriginCheck(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	created := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	assertStatus(t, created, http.StatusCreated)
	secret, _ := decode[map[string]any](t, created)["token"].(string)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/users", contentType: "application/json",
		body: newAdminBody, bearer: secret, noCookie: true,
		headers: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://outro.exemplo"},
	})

	assertStatus(t, resp, http.StatusCreated)
}

// O token também não dispensa o JSON: a exigência é do corpo, não da
// credencial.
func TestBearerTokenStillNeedsJSONContentType(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	created := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	secret, _ := decode[map[string]any](t, created)["token"].(string)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/users", contentType: "text/plain",
		body: newAdminBody, bearer: secret, noCookie: true,
	})

	assertStatus(t, resp, http.StatusUnsupportedMediaType)
}

// O push não usa cookie, e o agente que o chama pode estar em qualquer
// lugar, inclusive numa página: nem origem nem tipo de conteúdo o barram.
func TestPushIgnoresOriginAndContentType(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.send(t, browserRequest{
		method: http.MethodPost, path: "/api/v1/push/" + pushToken + "?status=down",
		contentType: "text/plain", body: "qualquer coisa",
		headers: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://outro.exemplo"},
	})

	assertStatus(t, resp, http.StatusOK)
	if got := s.lastSignal(t, id).Status; got != domain.StatusDown {
		t.Errorf("recorded status = %q, want down", got)
	}
}

// Quem gera cliente pela especificação precisa saber do 415 em toda rota
// que recebe corpo; senão o erro chega como um status que o cliente não
// previu.
func TestOpenAPIDocumentsUnsupportedMediaType(t *testing.T) {
	s := newServer(t)
	parsed := fetchSpec(t, s)

	for path, operations := range parsed.Paths {
		if strings.HasPrefix(path, "/api/v1/push/") {
			continue
		}
		for method, op := range operations {
			if _, hasBody := op["requestBody"]; !hasBody {
				continue
			}
			responses, _ := op["responses"].(map[string]any)
			if _, ok := responses["415"]; !ok {
				t.Errorf("%s %s takes a body but does not document 415", strings.ToUpper(method), path)
			}
		}
	}
}
