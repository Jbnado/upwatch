package api_test

import (
	"net/http"
	"testing"
)

// O login é limitado por origem, além do bloqueio por conta: palpites
// espalhados por muitas contas não chegam ao bloqueio de nenhuma.
func TestLoginIsRateLimitedPerOrigin(t *testing.T) {
	s := newServer(t)

	var last *http.Response
	for i := 0; ; i++ {
		last = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
			"username": "conta-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), "password": "senha-errada-porem-longa",
		})
		if last.StatusCode != http.StatusUnauthorized || i > 100 {
			break
		}
	}
	assertStatus(t, last, http.StatusTooManyRequests)
	if last.Header.Get("Retry-After") == "" {
		t.Error("429 sem Retry-After")
	}

	// O segundo passo divide o mesmo balde.
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/login/2fa",
		map[string]any{"challenge": "x", "code": "000000"}), http.StatusTooManyRequests)
}
