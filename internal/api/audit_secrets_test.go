package api_test

import (
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
)

// Este é o teste que dá sentido ao resto. Um log de auditoria que vaza
// segredo é pior que log nenhum: ele é copiado para fora da máquina, vai
// para coletor de terceiro e fica guardado por meses, de modo que a senha
// que apareceu numa linha passa a existir em muito mais lugares do que a
// base de dados que a guarda com hash.
//
// O passeio abaixo exercita todos os fluxos que registram linha, cada um
// com um segredo distinto e reconhecível, e exige que nenhum deles apareça
// em lugar nenhum do log — nem nas linhas de auditoria, nem nas outras.
func TestAuditNeverLogsSecrets(t *testing.T) {
	const (
		senhaInicial  = "senha-inicial-secretissima"
		senhaNova     = "senha-nova-secretissima"
		senhaDaOutra  = "senha-da-outra-secretissima"
		tokenMétricas = "token-de-metricas-secretissimo"
		setupErrado   = "palpite-de-setup-secretissimo"
	)

	s, reg := servidorComLog(t, func(o *api.Options) {
		o.MetricsToken = tokenMétricas
	})

	// Setup: token errado e token certo.
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "admin", "password": senhaInicial, "setup_token": setupErrado,
	}), http.StatusUnauthorized)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "admin", "password": senhaInicial, "setup_token": testSetupToken,
	}), http.StatusCreated)

	// Login errado e login certo.
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": senhaNova,
	}), http.StatusUnauthorized)
	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": senhaInicial,
	})
	assertStatus(t, resp, http.StatusOK)
	s.cookie = sessionCookie(resp)

	// Segundo fator: cadastro, código errado e código certo.
	segredoTOTP, recuperação := s.enrollWith(t, senhaInicial)
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": senhaInicial,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio := decode[map[string]any](t, resp)["challenge"].(string)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": "000000",
	}), http.StatusUnauthorized)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": senhaInicial,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio = decode[map[string]any](t, resp)["challenge"].(string)
	códigoTOTP := s.totpCode(t, segredoTOTP)
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": códigoTOTP,
	})
	assertStatus(t, resp, http.StatusOK)
	s.cookie = sessionCookie(resp)

	// Entrar com código de recuperação, que é o único segredo que a pessoa
	// digita de um papel e o único que produz linha de login.
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": senhaInicial,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio = decode[map[string]any](t, resp)["challenge"].(string)
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": recuperação[0],
	})
	assertStatus(t, resp, http.StatusOK)
	s.cookie = sessionCookie(resp)

	// Token de API: o segredo só aparece nesta resposta.
	resp = s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "token-do-ci"})
	assertStatus(t, resp, http.StatusCreated)
	criado := decode[map[string]any](t, resp)
	segredoToken := criado["token"].(string)
	id := strconv.FormatInt(int64(criado["id"].(float64)), 10)
	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+id, nil), http.StatusNoContent)

	// Outra conta, e o acesso dela trocado.
	resp = s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "observadora", "password": senhaDaOutra, "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)
	outroID := strconv.FormatInt(int64(decode[map[string]any](t, resp)["id"].(float64)), 10)
	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/users/"+outroID,
		map[string]any{"role": "admin"}), http.StatusOK)

	// Token de métricas errado.
	req, err := http.NewRequest(http.MethodGet, s.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenMétricas+"-errado")
	mresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	mresp.Body.Close()

	// Troca de senha por último: encerra a sessão.
	cookieUsado := s.cookie.Value
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": senhaInicial, "new_password": senhaNova,
	}), http.StatusNoContent)

	// O passeio tem que ter produzido log, senão este teste passaria
	// sozinho sem conferir nada.
	if len(reg.linhas()) == 0 {
		t.Fatal("o passeio não produziu nenhuma linha de auditoria")
	}

	segredos := map[string]string{
		"senha inicial":              senhaInicial,
		"senha nova":                 senhaNova,
		"senha da outra conta":       senhaDaOutra,
		"token de métricas":          tokenMétricas,
		"palpite de setup":           setupErrado,
		"token de configuração":      testSetupToken,
		"segredo TOTP":               segredoTOTP,
		"código TOTP":                códigoTOTP,
		"primeiro código de recuper": recuperação[0],
		"segredo do token de API":    segredoToken,
		"desafio do segundo passo":   desafio,
		"cookie de sessão":           cookieUsado,
	}
	log := reg.texto()
	for nome, segredo := range segredos {
		if segredo == "" {
			t.Errorf("o teste não capturou o segredo %q; ele deixaria de ser conferido", nome)
			continue
		}
		if strings.Contains(log, segredo) {
			t.Errorf("o log traz %s (%q):\n%s", nome, segredo, log)
		}
	}
}

// Sem conta identificada a linha precisa dizer isso, e não sair com o
// campo vazio: campo vazio num coletor passa por campo ausente, e a
// linha deixa de casar com o filtro de quem procura ação anônima.
func TestAuditCallsUnauthenticatedActorAnonymous(t *testing.T) {
	s, reg := servidorComLog(t, nil)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "invasor", "password": "uma-senha-bem-longa",
		"setup_token": "token-errado-de-proposito",
	}), http.StatusUnauthorized)

	linha := reg.comAção(t, "setup.token_errado")
	if !strings.Contains(linha, "ator="+atorAnônimoNoLog) {
		t.Errorf("linha não traz ator=%s: %s", atorAnônimoNoLog, linha)
	}
}

// Atrás de proxy reverso toda requisição chega do proxy. Registrar o
// endereço dele deixaria o log inútil justamente na instalação exposta:
// todas as linhas apontariam para o mesmo lugar. A origem segue a mesma
// regra dos limites por origem (ver originResolver).
func TestAuditRecordsClientOriginBehindTrustedProxy(t *testing.T) {
	s, reg := servidorComLog(t, func(o *api.Options) {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	})

	corpo := `{"username":"invasor","password":"uma-senha-bem-longa","setup_token":"errado"}`
	req, err := http.NewRequest(http.MethodPost, s.URL+"/api/v1/setup", strings.NewReader(corpo))
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	defer resp.Body.Close()
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "setup.token_errado")
	if !strings.Contains(linha, "origem=203.0.113.7") {
		t.Errorf("linha não traz a origem do cliente: %s", linha)
	}
}

// Quem não é proxy confiável não escolhe a própria origem. Ler o
// cabeçalho de qualquer um deixaria o atacante assinar cada tentativa com
// um endereço inventado, e o log viraria uma lista de endereços falsos —
// pior que não ter log, porque parece evidência.
func TestAuditIgnoresForwardedOriginFromUntrustedPeer(t *testing.T) {
	s, reg := servidorComLog(t, nil)

	corpo := `{"username":"invasor","password":"uma-senha-bem-longa","setup_token":"errado"}`
	req, err := http.NewRequest(http.MethodPost, s.URL+"/api/v1/setup", strings.NewReader(corpo))
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	defer resp.Body.Close()
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "setup.token_errado")
	if strings.Contains(linha, "203.0.113.7") {
		t.Errorf("a linha aceitou a origem que o cliente inventou: %s", linha)
	}
	if !strings.Contains(linha, "origem=127.0.0.1") {
		t.Errorf("linha não traz a origem real da conexão: %s", linha)
	}
}
