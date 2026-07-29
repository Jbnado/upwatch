package api

import (
	_ "embed"
	"net/http"
)

// openAPISpec é a especificação da API, embarcada no binário.
//
// Embarcar em vez de servir de disco significa que a documentação nunca
// diverge da versão em execução: quem consulta a especificação de uma
// instalação está lendo exatamente o contrato daquele binário.
//
//go:embed openapi.yaml
var openAPISpec []byte

// handleOpenAPI entrega a especificação.
//
// Sem autenticação de propósito: descobrir como autenticar é justamente o
// que se procura na documentação, e exigir credencial para lê-la criaria
// um ciclo. A especificação descreve o formato da API, não os dados da
// instalação.
func (a *API) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	if _, err := w.Write(openAPISpec); err != nil {
		writeStreamFailure(w, err)
	}
}
