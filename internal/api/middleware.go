package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bernardojoao/upwatch/internal/auth"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// SessionCookieName é o nome do cookie de sessão.
const SessionCookieName = "upwatch_session"

// contextKey evita colisão com chaves de outros pacotes no contexto.
type contextKey int

const userContextKey contextKey = iota

// userFrom recupera a conta autenticada da requisição.
func userFrom(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userContextKey).(domain.User)
	return u, ok
}

// mustUser recupera a conta em rotas já protegidas pelo middleware.
func mustUser(r *http.Request) domain.User {
	u, _ := userFrom(r.Context())
	return u
}

// securityHeaders aplica defesas que não custam nada e fecham classes
// inteiras de ataque.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Impede o navegador de adivinhar o tipo do conteúdo, o que
		// permitiria tratar uma resposta de dados como script.
		h.Set("X-Content-Type-Options", "nosniff")
		// Bloqueia embutir a interface em iframe alheio, fechando o
		// caminho de sequestro de cliques.
		h.Set("X-Frame-Options", "DENY")
		// Evita vazar a URL da instalação — que costuma ser interna — para
		// sites externos.
		h.Set("Referrer-Policy", "no-referrer")

		next.ServeHTTP(w, r)
	})
}

// recoverPanic transforma pânico em resposta de erro.
//
// Sem isto, um defeito num handler derrubaria o processo inteiro e pararia
// o monitoramento de todos os alvos junto com a interface.
func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			// Cliente desconectado no meio da resposta não é defeito.
			if errors.Is(r.Context().Err(), context.Canceled) {
				return
			}

			slog.Error("api: pânico no handler",
				"erro", p, "método", r.Method, "caminho", r.URL.Path)
			writeError(w, http.StatusInternalServerError, codeInternal, "erro interno")
		}()

		next.ServeHTTP(w, r)
	})
}

// authenticate exige sessão ou token válido.
//
// Aceita as duas formas na mesma rota para que a interface e um script
// consumam exatamente os mesmos endpoints, sem uma API paralela para
// automação.
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := a.resolveUser(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "autenticação necessária")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveUser identifica a conta pela credencial apresentada.
func (a *API) resolveUser(r *http.Request) (domain.User, error) {
	if secret, ok := bearerToken(r); ok {
		return a.auth.AuthenticateToken(r.Context(), secret)
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return domain.User{}, auth.ErrUnauthenticated
	}
	return a.auth.AuthenticateSession(r.Context(), cookie.Value)
}

// bearerToken extrai o segredo do cabeçalho Authorization.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "

	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}
