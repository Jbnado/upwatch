package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/totp"
)

// enroll liga o segundo fator da conta da sessão atual e devolve o
// segredo e os códigos de recuperação. Avança o relógio depois, para o
// próximo código não ser o mesmo da confirmação.
func (s *server) enroll(t *testing.T) (string, []string) {
	t.Helper()
	return s.enrollWith(t, adminPassword)
}

// enrollWith é enroll para uma conta com outra senha.
func (s *server) enrollWith(t *testing.T, password string) (string, []string) {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup", map[string]any{"password": password})
	assertStatus(t, resp, http.StatusOK)
	setup := decode[struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
	}](t, resp)
	if setup.Secret == "" || setup.URI == "" {
		t.Fatalf("setup = %+v, want segredo e URI", setup)
	}

	resp = s.do(t, http.MethodPost, "/api/v1/auth/2fa/enable", map[string]any{"code": s.totpCode(t, setup.Secret)})
	assertStatus(t, resp, http.StatusOK)
	codes := decode[struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}](t, resp).RecoveryCodes

	s.clock.Advance(totp.Period)
	return setup.Secret, codes
}

func (s *server) totpCode(t *testing.T, secret string) string {
	t.Helper()
	c, err := totp.Code(secret, s.clock.Now())
	if err != nil {
		t.Fatalf("totp.Code returned unexpected error: %v", err)
	}
	return c
}

type loginResp struct {
	Status    string `json:"status"`
	Challenge string `json:"challenge"`
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

// A senha certa de conta com segundo fator devolve o desafio, sem cookie;
// o segundo passo é que abre a sessão.
func TestLoginWithTOTPNeedsTheSecondStep(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	secret, _ := s.enroll(t)
	s.cookie = nil

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	if c := sessionCookie(resp); c != nil {
		t.Fatal("o primeiro passo entregou cookie de sessão")
	}
	got := decode[loginResp](t, resp)
	if got.Status != "two_factor_required" || got.Challenge == "" {
		t.Fatalf("login = %+v, want two_factor_required com desafio", got)
	}

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": got.Challenge, "code": "000000",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": got.Challenge, "code": s.totpCode(t, secret),
	})
	assertStatus(t, resp, http.StatusOK)
	s.cookie = sessionCookie(resp)
	if s.cookie == nil {
		t.Fatal("o segundo passo não entregou cookie de sessão")
	}
	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusOK)

	// O desafio serviu uma vez.
	s.clock.Advance(totp.Period)
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": got.Challenge, "code": s.totpCode(t, secret),
	})
	assertStatus(t, resp, http.StatusUnauthorized)
	if body := readBody(t, resp); !strings.Contains(body, `"field":"challenge"`) {
		t.Errorf("desafio reusado = %s, want field challenge", body)
	}
}

// Senha errada responde exatamente igual com e sem segundo fator.
func TestWrongPasswordLooksTheSameWithTOTP(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.criarConta(t, "sem-fator", "admin")
	s.enroll(t)
	s.cookie = nil

	com := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "senha-errada-porem-longa",
	})
	sem := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "sem-fator", "password": "senha-errada-porem-longa",
	})
	if com.StatusCode != sem.StatusCode {
		t.Errorf("status com fator = %d, sem = %d", com.StatusCode, sem.StatusCode)
	}
	if a, b := readBody(t, com), readBody(t, sem); a != b {
		t.Errorf("corpo com fator = %s, sem = %s", a, b)
	}
}

// Ligar o segundo fator pela API derruba as outras sessões da conta.
func TestEnablingTOTPRevokesOtherSessionsViaAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	outra := s.entrar(t, "admin", adminPassword)

	s.enroll(t)

	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusOK)
	assertStatus(t, s.como(outra).do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized)
}

func TestMeReportsTOTPEnabled(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.enroll(t)

	me := decode[map[string]any](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))
	if me["totp_enabled"] != true {
		t.Errorf("me = %v, want totp_enabled true", me)
	}
	if me["two_factor_enrollment_required"] != false {
		t.Errorf("me = %v, want two_factor_enrollment_required false", me)
	}
}

// Token de API segue funcionando para conta com segundo fator, inclusive
// com a exigência ligada.
func TestAPITokenIgnoresTOTP(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.Require2FAAdmin = true })
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup", map[string]any{"password": adminPassword})
	assertStatus(t, resp, http.StatusOK)
	s.enroll(t)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "ci"})
	assertStatus(t, resp, http.StatusCreated)
	token := decode[struct {
		Token string `json:"token"`
	}](t, resp).Token

	assertStatus(t, s.withToken(t, token, http.MethodGet, "/api/v1/monitors", nil), http.StatusOK)
}

// Com a exigência ligada, administrador sem segundo fator só alcança o
// cadastro, a própria conta e o logout; terminado o cadastro, a mesma
// sessão passa a alcançar tudo.
func TestRequire2FAAdminRestrictsUntilEnrollment(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.Require2FAAdmin = true })
	s.setup(t)

	me := decode[map[string]any](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))
	if me["two_factor_enrollment_required"] != true {
		t.Fatalf("me = %v, want two_factor_enrollment_required true", me)
	}

	for _, rota := range []struct{ metodo, caminho string }{
		{http.MethodGet, "/api/v1/monitors"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/auth/password"},
		{http.MethodGet, "/api/v1/auth/tokens"},
	} {
		resp := s.do(t, rota.metodo, rota.caminho, map[string]any{})
		assertStatus(t, resp, http.StatusForbidden)
		if body := readBody(t, resp); !strings.Contains(body, `"code":"two_factor_enrollment_required"`) {
			t.Errorf("%s %s = %s, want code two_factor_enrollment_required", rota.metodo, rota.caminho, body)
		}
	}
	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/auth/2fa", nil), http.StatusOK)

	s.enroll(t)

	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/monitors", nil), http.StatusOK)
	me = decode[map[string]any](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))
	if me["two_factor_enrollment_required"] != false {
		t.Errorf("me = %v depois do cadastro, want two_factor_enrollment_required false", me)
	}
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/logout", nil), http.StatusNoContent)
}

// A exigência é de administrador: observador entra como sempre.
func TestRequire2FAAdminLeavesViewersAlone(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.Require2FAAdmin = true })
	s.setup(t)
	s.enroll(t)
	s.comoObservador(t)

	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/monitors", nil), http.StatusOK)
}

// Sem a exigência, nada muda para quem não cadastrou.
func TestWithoutRequire2FAAdminNothingIsRestricted(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	assertStatus(t, s.do(t, http.MethodGet, "/api/v1/monitors", nil), http.StatusOK)
}

func TestAdminResetsAnotherUsersTOTP(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	adminCookie := s.cookie
	id := s.criarConta(t, "operadora", "admin")

	op := s.como(s.entrar(t, "operadora", "senha-de-teste-12345"))
	op.enrollWith(t, "senha-de-teste-12345")

	users := decode[struct {
		Items []map[string]any `json:"items"`
	}](t, s.como(adminCookie).do(t, http.MethodGet, "/api/v1/users", nil)).Items
	for _, u := range users {
		if want := u["username"] == "operadora"; u["totp_enabled"] != want {
			t.Errorf("%v: totp_enabled = %v, want %v", u["username"], u["totp_enabled"], want)
		}
	}

	resp := s.como(adminCookie).do(t, http.MethodDelete, "/api/v1/users/"+itoa(id)+"/2fa", nil)
	assertStatus(t, resp, http.StatusNoContent)

	// As sessões da conta caem com o reset.
	assertStatus(t, op.do(t, http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized)

	// E ela volta a entrar só com a senha.
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "operadora", "password": "senha-de-teste-12345",
	})
	assertStatus(t, resp, http.StatusOK)
	if sessionCookie(resp) == nil {
		t.Error("login depois do reset não abriu sessão")
	}
}

func TestAdminCannotResetOwnTOTPThroughUsers(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.enroll(t)
	me := decode[struct {
		ID int64 `json:"id"`
	}](t, s.do(t, http.MethodGet, "/api/v1/auth/me", nil))

	resp := s.do(t, http.MethodDelete, "/api/v1/users/"+itoa(me.ID)+"/2fa", nil)
	assertStatus(t, resp, http.StatusConflict)
}

func TestViewerCannotResetTOTP(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.comoObservador(t)

	resp := s.do(t, http.MethodDelete, "/api/v1/users/1/2fa", nil)
	assertStatus(t, resp, http.StatusForbidden)
}

func TestDisableTOTPViaAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	secret, _ := s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/2fa/disable", map[string]any{
		"password": adminPassword, "code": "000000",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/2fa/disable", map[string]any{
		"password": adminPassword, "code": s.totpCode(t, secret),
	})
	assertStatus(t, resp, http.StatusNoContent)

	st := decode[map[string]any](t, s.do(t, http.MethodGet, "/api/v1/auth/2fa", nil))
	if st["enabled"] != false {
		t.Errorf("status = %v, want enabled false", st)
	}
}

// Começar o cadastro pede a senha atual: com a sessão só, quem roubou o
// cookie cadastraria o próprio aplicativo.
func TestTOTPSetupNeedsThePassword(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup", map[string]any{}), http.StatusUnauthorized)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup",
		map[string]any{"password": "senha-errada-porem-longa"}), http.StatusUnauthorized)

	resp := s.do(t, http.MethodGet, "/api/v1/auth/2fa", nil)
	assertStatus(t, resp, http.StatusOK)
	if st := decode[map[string]any](t, resp); st["pending"] != false {
		t.Errorf("estado = %v, want nenhum cadastro pendente", st)
	}

	// As erradas contam no bloqueio da conta.
	for range 3 {
		s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup", map[string]any{"password": "senha-errada-porem-longa"})
	}
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/2fa/setup",
		map[string]any{"password": adminPassword}), http.StatusTooManyRequests)
}
