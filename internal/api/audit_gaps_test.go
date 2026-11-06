package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/audit"
)

// Um administrador que apaga a conta de outro não deixava rastro nenhum — e
// a conta apagada é justamente a que não pode mais ser consultada para
// reconstruir o que havia. O handler vizinho, que troca papel e escopo,
// sempre registrou.
func TestAuditLogsDeletingAnAccount(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "observadora", "password": "uma-senha-bem-longa", "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)
	id := decode[idResp](t, resp).ID

	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/users/"+itoa(id), nil), http.StatusNoContent)

	linha := reg.comAção(t, audit.AçãoUsuárioRemovido)
	// O alvo é o nome, não o id: depois de apagar não há mais linha para
	// consultar, e um número não responde o que a auditoria serve para
	// responder.
	exigeCampos(t, linha, "admin", "observadora", "127.0.0.1")
}

// O /push é o único endpoint sem login com credencial própria, e palpites
// nele não apareciam em lugar nenhum.
func TestAuditLogsAWrongPushToken(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/token-que-nao-existe", nil),
		http.StatusUnauthorized)

	linha := reg.comAção(t, audit.AçãoPushTokenErrado)
	if strings.Contains(linha, "token-que-nao-existe") {
		t.Errorf("a linha trouxe o token oferecido: %s", linha)
	}
	exigeCampos(t, linha, atorAnônimoNoLog, "/api/v1/push/{token}", "127.0.0.1")
}

// Quem está com um cookie roubado e tenta adivinhar a senha atual aqui
// gerava zero linhas: a rota de login registra o palpite, esta não.
func TestAuditLogsAWrongCurrentPassword(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": "senha-errada-porem-longa", "new_password": "outra-senha-bem-longa",
	}), http.StatusUnauthorized)

	linha := reg.comAção(t, audit.AçãoSenhaAtualErrada)
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
	for _, segredo := range []string{"senha-errada-porem-longa", "outra-senha-bem-longa"} {
		if strings.Contains(reg.texto(), segredo) {
			t.Errorf("o log trouxe uma senha: %s", reg.texto())
		}
	}
}

// O 429 por tentativas só era registrado em /auth/login. As mesmas
// tentativas contadas na troca de senha viravam 429 silencioso, e quem lia
// o log via uma conta travada sem causa.
func TestAuditLogsTheAccountLockFromThePasswordChange(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	var last *http.Response
	for i := 0; i < 10; i++ {
		last = s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
			"current_password": "senha-errada-porem-longa", "new_password": "outra-senha-bem-longa",
		})
		if last.StatusCode == http.StatusTooManyRequests {
			break
		}
	}
	assertStatus(t, last, http.StatusTooManyRequests)

	linha := reg.comAção(t, audit.AçãoContaBloqueada)
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

// O ator pode vir do corpo da requisição — o login registra o nome
// oferecido —, e o corpo aceito vai a 1 MiB. Não há forja de linha, mas
// trinta requisições de rajada com um nome enorme enchem o coletor de quem
// paga por volume.
func TestAuditTruncatesALongActor(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	enorme := strings.Repeat("n", 5000)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": enorme, "password": "uma-senha-bem-longa",
	}), http.StatusUnauthorized)

	log := reg.texto()
	if strings.Contains(log, strings.Repeat("n", audit.MaxAtorLength+2)) {
		t.Errorf("o ator entrou no log sem teto (%d caracteres de nome)", len(enorme))
	}
}
