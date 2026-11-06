package api_test

import (
	"net/http"
	"strconv"
	"testing"
)

// As ações de 2FA já eram registradas antes desta mudança, mas em outro
// formato ("conta=") e no logger padrão do processo. Duas formas de
// escrever evento de segurança no mesmo log é o que torna o log
// inutilizável: quem coleta precisa de um filtro por ação só, e quem
// audita precisa que ator, alvo e origem estejam sempre nos mesmos
// campos. Além disso, as linhas antigas não traziam origem nenhuma.
func TestAuditLogsTwoFactorEnabled(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	s.enroll(t)

	linha := reg.comAção(t, "2fa.ligada")
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

func TestAuditLogsRecoveryCodesReplaced(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	segredo, _ := s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/2fa/recovery-codes", map[string]any{
		"password": adminPassword, "code": s.totpCode(t, segredo),
	})
	assertStatus(t, resp, http.StatusOK)

	linha := reg.comAção(t, "2fa.recuperacao_trocada")
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

func TestAuditLogsTwoFactorDisabled(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	segredo, _ := s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/2fa/disable", map[string]any{
		"password": adminPassword, "code": s.totpCode(t, segredo),
	})
	assertStatus(t, resp, http.StatusNoContent)

	linha := reg.comAção(t, "2fa.desligada")
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

// Administrador desligando o 2FA de outra pessoa é o caminho legítimo de
// recuperação e, ao mesmo tempo, o caminho que um administrador
// comprometido usa para entrar na conta de alguém. A linha tem que dizer
// quem desligou e de quem.
func TestAuditLogsTwoFactorResetByAdmin(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	const senhaDaOutra = "uma-senha-bem-longa"
	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "observadora", "password": senhaDaOutra, "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)
	id := strconv.FormatInt(int64(decode[map[string]any](t, resp)["id"].(float64)), 10)

	// Só se desliga o que está ligado: a conta alvo precisa cadastrar o
	// segundo fator antes, e isso se faz pela sessão dela.
	cookieDoAdmin := s.cookie
	resp = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "observadora", "password": senhaDaOutra,
	})
	assertStatus(t, resp, http.StatusOK)
	s.cookie = sessionCookie(resp)
	s.enrollWith(t, senhaDaOutra)
	s.cookie = cookieDoAdmin

	resp = s.do(t, http.MethodDelete, "/api/v1/users/"+id+"/2fa", nil)
	assertStatus(t, resp, http.StatusNoContent)

	linha := reg.comAção(t, "2fa.desligada_por_admin")
	exigeCampos(t, linha, "admin", "observadora", "127.0.0.1")
}

// ---------- contorno do aplicativo autenticador ----------

// Entrar com código de recuperação é passar por cima do aplicativo
// autenticador. É o que faz quem perdeu o celular e é o que faz quem
// roubou a lista de códigos, e das duas maneiras a conta entra sem o
// segundo fator que a instalação exige.
//
// O login certo com o aplicativo não gera linha de propósito: registrar
// todo login encheria o log e esconderia o que importa. Este gera, e a
// linha precisa dizer qual conta — a sessão ainda não existia quando o
// pedido chegou, então não há conta no contexto da requisição.
func TestAuditLogsRecoveryCodeLogin(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	_, recuperação := s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio := decode[map[string]any](t, resp)["challenge"].(string)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": recuperação[0],
	})
	assertStatus(t, resp, http.StatusOK)

	linha := reg.comAção(t, "2fa.recuperacao_usada")
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

// Login com o aplicativo não pode sair como recuperação: a linha existe
// para separar quem entrou pelo segundo fator de quem entrou por cima
// dele, e um login comum marcado como contorno faria o log acusar todo
// mundo.
func TestAuditDoesNotCallAnAuthenticatorLoginARecovery(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	segredo, _ := s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio := decode[map[string]any](t, resp)["challenge"].(string)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": s.totpCode(t, segredo),
	})
	assertStatus(t, resp, http.StatusOK)

	reg.recusaAção(t, "2fa.recuperacao_usada")
}

// Código de recuperação recusado é tentativa, não contorno. Marcá-lo
// como contorno diria que alguém entrou quando ninguém entrou, e é essa
// a linha que faria alguém revogar as sessões de quem errou de digitar.
func TestAuditDoesNotLogARefusedRecoveryCode(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio := decode[map[string]any](t, resp)["challenge"].(string)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": "aaaa-bbbb-cccc-dddd",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	reg.comAção(t, "login.codigo_errado")
	reg.recusaAção(t, "2fa.recuperacao_usada")
}
