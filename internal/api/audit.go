package api

import "net/http"

// Ações registradas no log de auditoria.
//
// Nomes estáveis, em minúsculas e separados por ponto, para servirem de
// filtro: quem coleta o log procura por "ação=login.senha_errada", e um
// texto reescrito não pode quebrar esse filtro.
const (
	ageLoginSenhaErrada  = "login.senha_errada"
	ageLoginCódigoErrado = "login.codigo_errado"
	ageLimiteBloqueio    = "limite.bloqueio"
	ageContaBloqueada    = "login.conta_bloqueada"

	ageTokenCriado   = "token.criado"
	ageTokenRevogado = "token.revogado"

	ageSenhaTrocada       = "senha.trocada"
	ageUsuárioCriado      = "usuario.criado"
	ageAcessoTrocado      = "usuario.acesso_trocado"
	ageSetupTokenErrado   = "setup.token_errado"
	ageMetricsTokenErrado = "metrics.token_errado"

	age2FALigada            = "2fa.ligada"
	age2FADesligada         = "2fa.desligada"
	age2FARecuperaçãoNova   = "2fa.recuperacao_trocada"
	age2FADesligadaPorAdmin = "2fa.desligada_por_admin"
)

// atorAnônimo é o ator das ações sem conta identificada.
const atorAnônimo = "anônimo"

// audit grava um evento de segurança.
//
// Um funil só, em vez de slog espalhado pelos handlers, por dois motivos:
// as linhas saem todas com os mesmos campos, que é o que torna o log
// filtrável; e existe um lugar único para garantir que segredo nenhum
// entre, o que uma chamada solta em cada handler não garante.
//
// Nada aqui recebe senha, código ou token: a assinatura só aceita ação e
// alvo, e quem chama passa identificador ou nome de rota. Confira em
// audit_secrets_test.go, TestAuditNeverLogsSecrets.
func (a *API) audit(r *http.Request, ação, alvo string) {
	a.auditAs(r, atorDe(r), ação, alvo)
}

// auditAs grava o evento com o ator informado, para o caso em que não há
// conta autenticada mas se sabe por qual delas se tentou passar — o login
// errado é o exemplo.
func (a *API) auditAs(r *http.Request, ator, ação, alvo string) {
	if ator == "" {
		ator = atorAnônimo
	}
	a.logger.Info("evento de segurança",
		"ação", ação,
		"ator", ator,
		"alvo", alvo,
		"origem", a.origins.origin(r),
	)
}

// atorDe identifica a conta da requisição, ou devolve o ator anônimo.
func atorDe(r *http.Request) string {
	if u, ok := userFrom(r.Context()); ok {
		return u.Username
	}
	return atorAnônimo
}
