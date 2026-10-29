package server

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy limita de onde a página carrega o que carrega.
//
// Tudo vem do próprio binário, então 'self' basta para script, estilo e
// conexão (inclusive o fluxo de eventos). As exceções são data: em imagem,
// pelo ícone da aba, que mora inline no index.html, e em fonte, porque o
// vite embute as fontes pequenas no CSS em vez de servi-las à parte.
//
// Sem 'unsafe-inline' em lugar nenhum: é ele que faria um XSS futuro
// virar execução, e a interface não precisa — o estilo dinâmico do React e
// do gráfico vai pelo CSSOM, que a CSP não restringe.
//
// frame-ancestors fecha o iframe alheio (sequestro de clique); base-uri e
// object-src fecham dois desvios clássicos de quem já conseguiu injetar
// HTML; form-action impede um formulário injetado de mandar a senha
// digitada para fora.
const contentSecurityPolicy = "default-src 'self'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"object-src 'none'; " +
	"form-action 'self'"

// hstsValue vale dois anos, o prazo recomendado para quem já serve só em
// HTTPS: curto demais e a proteção caduca entre uma visita e outra.
const hstsValue = "max-age=63072000; includeSubDomains"

// Options ajusta a composição.
type Options struct {
	// SecureCookies indica que a instalação é servida em HTTPS, e liga o
	// HSTS. Mesmo critério do cookie, porque o HSTS é uma promessa difícil
	// de desfazer: por dois anos, o navegador recusa HTTP no host e em
	// todos os subdomínios dele. Quem ainda serve em HTTP puro na rede
	// local não pode receber essa promessa sem ter pedido.
	SecureCookies bool
}

// securityHeaders aplica os cabeçalhos de defesa a toda resposta, da API,
// da interface e da página pública.
//
// Mora aqui, e não em cada lado, porque é aqui que todo caminho passa: a
// interface e a página pública são servidas fora do roteador da API, e
// ficavam sem nenhum.
func securityHeaders(next http.Handler, opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		// Redundante com frame-ancestors para quem entende CSP; fica para
		// o navegador que não entende.
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", referrerPolicy(r.URL.Path))
		if opts.SecureCookies {
			h.Set("Strict-Transport-Security", hstsValue)
		}

		next.ServeHTTP(w, r)
	})
}

// referrerPolicy escolhe quanto da URL vai junto quando alguém sai da
// página por um link.
//
// Na interface, só a origem, e só para HTTPS: o caminho de uma tela de
// administração não interessa a ninguém lá fora. Na página pública, nada:
// ela é aberta por gente de fora, que não precisa saber de onde veio.
func referrerPolicy(path string) string {
	if path == "/status" || strings.HasPrefix(path, "/status/") {
		return "no-referrer"
	}
	return "strict-origin-when-cross-origin"
}
