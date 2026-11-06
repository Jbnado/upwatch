package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/server"
	"github.com/Jbnado/upwatch/internal/web"
)

func headersOf(t *testing.T, h http.Handler, path string) http.Header {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Header()
}

// A interface e a página pública são servidas fora do roteador da API, e
// saíam sem cabeçalho nenhum. A composição real, com a interface embarcada,
// é o que se confere.
func TestEveryResponseCarriesSecurityHeaders(t *testing.T) {
	mux := server.New(api.New(api.Options{}), web.Handler(), server.Options{})

	for _, path := range []string{"/", "/monitors/7", "/status", "/status/acme", "/healthz", "/api/v1/setup"} {
		t.Run(path, func(t *testing.T) {
			h := headersOf(t, mux, path)

			for header, want := range map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
			} {
				if got := h.Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if h.Get("Referrer-Policy") == "" {
				t.Error("Referrer-Policy is missing")
			}
			if h.Get("Content-Security-Policy") == "" {
				t.Error("Content-Security-Policy is missing")
			}
		})
	}
}

func TestContentSecurityPolicyIsStrict(t *testing.T) {
	mux := server.New(marcador("api"), marcador("interface"), server.Options{})

	csp := headersOf(t, mux, "/").Get("Content-Security-Policy")

	for _, directive := range []string{
		"default-src 'self'", "frame-ancestors 'none'", "base-uri 'none'",
		"object-src 'none'", "form-action 'self'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q lacks %q", csp, directive)
		}
	}
	// 'unsafe-inline' e 'unsafe-eval' são o que transformaria um XSS em
	// execução; a interface não precisa de nenhum dos dois.
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP %q allows %q", csp, forbidden)
		}
	}
}

// A página pública é aberta por gente de fora, que não precisa saber de
// onde veio; a interface entrega só a origem.
func TestReferrerPolicyByArea(t *testing.T) {
	mux := server.New(marcador("api"), marcador("interface"), server.Options{})

	for path, want := range map[string]string{
		"/":               "strict-origin-when-cross-origin",
		"/monitors/7":     "strict-origin-when-cross-origin",
		"/status":         "no-referrer",
		"/status/acme":    "no-referrer",
		"/status-pages":   "strict-origin-when-cross-origin",
		"/statusqualquer": "strict-origin-when-cross-origin",
	} {
		if got := headersOf(t, mux, path).Get("Referrer-Policy"); got != want {
			t.Errorf("%s: Referrer-Policy = %q, want %q", path, got, want)
		}
	}
}

// O HSTS vale para o host e os subdomínios por dois anos; vai só para quem
// já declarou servir em HTTPS ligando o cookie Secure.
func TestHSTSFollowsSecureCookies(t *testing.T) {
	plain := server.New(marcador("api"), marcador("interface"), server.Options{})
	if got := headersOf(t, plain, "/").Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS = %q without secure cookies, want none", got)
	}

	secure := server.New(marcador("api"), marcador("interface"), server.Options{SecureCookies: true})
	if got := headersOf(t, secure, "/").Get("Strict-Transport-Security"); got != "max-age=63072000; includeSubDomains" {
		t.Errorf("HSTS = %q with secure cookies", got)
	}
}
