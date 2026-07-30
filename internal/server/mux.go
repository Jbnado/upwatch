// Package server compõe a API e a interface num handler só.
//
// Existe por causa de um defeito concreto: a composição vivia no main
// com uma lista fixa de prefixos, e acrescentar uma rota à API não a
// tornava alcançável — a requisição caía no atendimento da interface, que
// devolve o HTML da aplicação com status 200. Nada falha alto nesse
// caminho: quem chama recebe uma página em vez do JSON, e descobre pelo
// erro de parse do outro lado.
//
// Aqui a decisão de quem atende cada caminho fica ao lado de um teste que
// percorre as rotas registradas e confere que todas chegam à API.
package server

import (
	"net/http"
	"strings"
)

// apiPrefixes são os caminhos atendidos pela API, e não pela interface.
//
// Lista curta de propósito: tudo o que é programático mora sob /api/, e
// as exceções são endpoints que ferramentas de infraestrutura esperam na
// raiz — orquestrador consulta /healthz, Prometheus raspa /metrics, e
// nenhum dos dois aceita um prefixo inventado.
var apiPrefixes = []string{"/api/", "/healthz", "/metrics"}

// Owns informa se o caminho pertence à API.
func Owns(path string) bool {
	for _, p := range apiPrefixes {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) {
				return true
			}
			continue
		}
		if path == p {
			return true
		}
	}
	return false
}

// New encaminha a API e entrega o resto à interface.
//
// A interface fica no fim porque ela é a única que atende caminho
// desconhecido: uma rota de aplicação de página única precisa devolver o
// HTML para qualquer endereço, e por isso não pode julgar o que é dela.
func New(apiHandler, spa http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Owns(r.URL.Path) {
			apiHandler.ServeHTTP(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	})
}
