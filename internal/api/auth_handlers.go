package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/domain"
)

// handleSetupStatus informa se o assistente de primeiro acesso deve
// aparecer.
func (a *API) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	need, err := a.auth.NeedsSetup(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": need})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetup cria a conta inicial.
func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if !decodeJSON(w, r, &body) {
		return
	}

	u, err := a.auth.CreateInitialAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// handleLogin abre uma sessão e devolve o cookie.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if !decodeJSON(w, r, &body) {
		return
	}

	secret, err := a.auth.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	http.SetCookie(w, a.sessionCookie(secret, a.clock.Now().Add(a.sessionTTL)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleLogout encerra a sessão e apaga o cookie.
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if err := a.auth.Logout(r.Context(), cookie.Value); err != nil {
			writeStoreError(w, err)
			return
		}
	}

	// Expiração no passado instrui o navegador a descartar o cookie.
	http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0)))
	w.WriteHeader(http.StatusNoContent)
}

// sessionCookie monta o cookie com as defesas aplicadas.
func (a *API) sessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:  SessionCookieName,
		Value: value,
		Path:  "/",
		// HttpOnly impede que script na página leia a sessão, de modo que
		// uma falha de injeção não vire tomada de conta.
		HttpOnly: true,
		// Lax fecha o caminho mais comum de requisição forjada entre sites
		// sem quebrar a navegação normal a partir de um link.
		SameSite: http.SameSiteLaxMode,
		Secure:   a.secureCookies,
		Expires:  expires,
	}
}

// handleMe devolve a conta autenticada.
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, mustUser(r))
}

// handleChangePassword troca a senha e encerra as sessões abertas.
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	if err := a.auth.ChangePassword(r.Context(), mustUser(r).ID, body.Current, body.Next); err != nil {
		writeStoreError(w, err)
		return
	}

	// A própria sessão desta requisição foi encerrada junto; apagar o
	// cookie evita o cliente insistir com uma credencial já inválida.
	http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0)))
	w.WriteHeader(http.StatusNoContent)
}

// handleListTokens lista as credenciais programáticas da conta.
func (a *API) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.auth.ListTokens(r.Context(), mustUser(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if tokens == nil {
		tokens = []domain.APIToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tokens})
}

// handleCreateToken emite uma credencial e devolve o segredo.
//
// Esta é a única resposta em que o segredo aparece; a listagem mostra
// apenas o prefixo, e nada permite reconstruí-lo depois.
func (a *API) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string     `json:"name"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	tok, secret, err := a.auth.IssueToken(r.Context(), mustUser(r).ID, body.Name, body.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         tok.ID,
		"name":       tok.Name,
		"prefix":     tok.Prefix,
		"created_at": tok.CreatedAt,
		"expires_at": tok.ExpiresAt,
		"token":      secret,
		"warning":    "guarde este valor agora; ele não pode ser recuperado depois",
	})
}

// handleRevokeToken apaga uma credencial da própria conta.
func (a *API) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.auth.RevokeToken(r.Context(), mustUser(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathID lê o identificador numérico da rota.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")

	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeFieldError(w, "id", "identificador inválido")
		return 0, false
	}
	return id, true
}
