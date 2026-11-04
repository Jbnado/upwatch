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
