package api

import (
	"net/http"

	"github.com/Jbnado/upwatch/internal/audit"
)

// audit grava um evento de segurança.
//
// Um funil só, em vez de slog espalhado pelos handlers, por dois motivos:
// as linhas saem todas com os mesmos campos, que é o que torna o log
// filtrável; e existe um lugar único para garantir que segredo nenhum
// entre, o que uma chamada solta em cada handler não garante.
//
// Os nomes das ações e a escrita da linha vivem no pacote audit, que a
// linha de comando também usa: ela desliga a verificação em duas etapas
// com o servidor parado, e essa ação tem de sair no mesmo formato que o
// resto para cair no mesmo filtro.
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
	audit.Registra(a.logger, audit.Evento{
		Ator:   ator,
		Ação:   ação,
		Alvo:   alvo,
		Origem: a.origins.origin(r),
	})
}

// atorDe identifica a conta da requisição, ou devolve o ator anônimo.
func atorDe(r *http.Request) string {
	if u, ok := userFrom(r.Context()); ok {
		return u.Username
	}
	return audit.AtorAnônimo
}
