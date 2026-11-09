package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
)

// pushPattern é o caminho do sinal de push, que fica fora da conferência
// de origem.
//
// O push não lê cookie: a credencial é o segredo no próprio caminho, e
// quem conhece o segredo já pode mandar o sinal de qualquer lugar. Conferir
// a origem ali só quebraria o agente que reporta de dentro de uma página.
const pushPattern = "/api/v1/push/{token}"

var errNotAbsolute = errors.New("o endereço precisa de esquema e host, como https://status.exemplo.com")

// newOriginCheck monta a conferência de origem das requisições que mudam
// estado.
//
// É a do pacote padrão: o navegador informa em Sec-Fetch-Site de onde a
// requisição partiu, e só same-origin ou none (endereço digitado) passam.
// Sem esse cabeçalho, vale o Origin, comparado com o Host da requisição.
// Sem nenhum dos dois não é navegador, e não há cookie alheio a proteger.
//
// O endereço público entra como origem confiável para o caso do proxy
// que reescreve o Host: num navegador antigo, sem Sec-Fetch-Site, o Origin
// traria o endereço externo e o Host o interno, e o login seria recusado.
func newOriginCheck(publicURL string) *http.CrossOriginProtection {
	check := http.NewCrossOriginProtection()
	check.AddInsecureBypassPattern(pushPattern)

	if publicURL == "" {
		return check
	}
	u, err := url.Parse(publicURL)
	if err == nil && (u.Scheme == "" || u.Host == "") {
		err = errNotAbsolute
	}
	if err == nil {
		err = check.AddTrustedOrigin(u.Scheme + "://" + u.Host)
	}
	if err != nil {
		// Só registra: o endereço público serve também ao feed, e um valor
		// malformado não pode impedir o processo de subir.
		slog.Warn("api: UPWATCH_PUBLIC_URL fora da conferência de origem; "+
			"vale só o Host da requisição", "valor", publicURL, "erro", err)
	}
	return check
}

// sameOrigin recusa a requisição que muda estado vinda de outra origem.
//
// O cookie SameSite=Lax separa sites, não origens; esta conferência
// separa origens. Vale também para login e primeiro acesso, que ainda não
// têm sessão: a sessão que eles abrem é tão sensível quanto a que já
// existe.
//
// Token Bearer fica de fora. O navegador não anexa Authorization por conta
// própria, então não há credencial ambiente a aproveitar, e a automação
// que roda num navegador continua funcionando.
func (a *API) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); !ok {
			if err := a.originCheck.Check(r); err != nil {
				writeError(w, http.StatusForbidden, codeForbidden,
					"requisição de outra origem recusada")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
