package api_test

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
)

func TestAuditSeparatesLoginLockoutScopes(t *testing.T) {
	for _, tc := range []struct{ scope, action string }{
		{"par", "login.par_bloqueado"},
		{"origem", "login.origem_bloqueada"},
		{"conta", "login.conta_bloqueada"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			s, reg := servidorComLog(t, func(o *api.Options) {
				o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
			})
			s.setup(t)
			login := func(origin, name string, status int) {
				t.Helper()
				req, err := http.NewRequest(http.MethodPost, s.URL+"/api/v1/auth/login",
					strings.NewReader(fmt.Sprintf(`{"username":%q,"password":"senha-errada-porem-longa"}`, name)))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Forwarded-For", origin)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { resp.Body.Close() })
				assertStatus(t, resp, status)
				if status == http.StatusTooManyRequests && resp.Header.Get("Retry-After") == "" {
					t.Fatal("bloqueio sem Retry-After")
				}
			}
			switch tc.scope {
			case "par":
				for range 5 {
					login("198.51.100.1", "inexistente", http.StatusUnauthorized)
				}
				login("198.51.100.1", "inexistente", http.StatusTooManyRequests)
			case "origem":
				for i := range 20 {
					login("198.51.100.1", fmt.Sprintf("conta-%d", i), http.StatusUnauthorized)
				}
				login("198.51.100.1", "outra-conta", http.StatusTooManyRequests)
			case "conta":
				for i := range 3 {
					for range 5 {
						login(fmt.Sprintf("198.51.100.%d", i+1), "inexistente", http.StatusUnauthorized)
					}
				}
				login("198.51.100.4", "inexistente", http.StatusTooManyRequests)
			}
			reg.comAção(t, tc.action)
		})
	}
}

func TestFailedLoginFromOneOriginDoesNotLockAnother(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})
	s.setup(t)
	login := func(origin, password string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, s.URL+"/api/v1/auth/login",
			strings.NewReader(`{"username":"admin","password":"`+password+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	for range 5 {
		assertStatus(t, login("198.51.100.10", "senha-errada-porem-longa"), http.StatusUnauthorized)
	}
	assertStatus(t, login("198.51.100.10", adminPassword), http.StatusTooManyRequests)
	assertStatus(t, login("198.51.100.11", adminPassword), http.StatusOK)
}
