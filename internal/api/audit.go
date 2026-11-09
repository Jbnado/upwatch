package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/audit"
	"github.com/Jbnado/upwatch/internal/auth"
)

func (a *API) auditLoginBlock(r *http.Request, username, target string, err error) {
	action := audit.AçãoContaBloqueada
	var limit *auth.AttemptLimitError
	if errors.As(err, &limit) {
		if username == "" {
			username = limit.Username
		}
		switch limit.Scope {
		case auth.AttemptScopePair:
			action = audit.AçãoParBloqueado
		case auth.AttemptScopeOrigin:
			action = audit.AçãoOrigemBloqueada
		}
	}
	a.auditAs(r, username, action, target)
}

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

// rotaDesconhecida é a rota de quem chegou sem casar com nenhuma.
const rotaDesconhecida = "(sem rota)"

// rotaDe identifica a rota pelo padrão com que ela foi registrada, e
// não pelo caminho que chegou na requisição.
//
// A diferença é um segredo. O caminho do /push carrega o token do
// monitor — decisão declarada, porque é assim que um cron reporta sem
// credencial de interface —, então registrar o caminho instanciado faz
// o funil escrever exatamente o que ele existe para manter fora:
// "/api/v1/push/<token-real>" no lugar de "/api/v1/push/{token}".
//
// Sem padrão devolve rotaDesconhecida, e nunca o caminho. Cair no
// caminho cru seria cair justamente no vazamento que esta função existe
// para fechar, e o caso é alcançável: recoverPanic é middleware do mux,
// que o chi roda ANTES de procurar a rota, então um pânico em qualquer
// middleware global chega aqui sem rota casada. Perder o endereço do
// defeito custa a quem investiga; perder o segredo custa a quem é dono
// dele.
func rotaDe(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if padrão := rc.RoutePattern(); padrão != "" {
			return padrão
		}
	}
	return rotaDesconhecida
}

// atorDe identifica a conta da requisição, ou devolve o ator anônimo.
func atorDe(r *http.Request) string {
	if u, ok := userFrom(r.Context()); ok {
		return u.Username
	}
	return audit.AtorAnônimo
}
