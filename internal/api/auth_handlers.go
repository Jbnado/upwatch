package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/audit"
	"github.com/Jbnado/upwatch/internal/auth"
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

// setupRequest é o corpo do assistente: as credenciais da conta e o
// token que prova acesso ao servidor.
type setupRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	SetupToken string `json:"setup_token"`
}

// handleSetup cria a conta inicial.
//
// A ordem das conferências importa. Instalação já configurada responde
// 409 antes de olhar o token: assim o token deixa de valer quando a conta
// passa a existir, e uma instância configurada antes de ele existir (que
// não tem token nenhum) continua respondendo como sempre.
func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body setupRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	need, err := a.auth.NeedsSetup(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !need {
		writeStoreError(w, auth.ErrSetupComplete)
		return
	}

	// Token vazio do lado do servidor recusa tudo, inclusive o corpo sem
	// token: comparar "" com "" abriria o assistente justamente quando
	// alguém esqueceu de configurá-lo.
	if a.setupToken == "" || !subtleEqual(body.SetupToken, a.setupToken) {
		a.audit(r, audit.AçãoSetupTokenErrado, "/setup")

		var resp errorBody
		resp.Error.Code = codeUnauthorized
		resp.Error.Message = "token de configuração inválido; ele está no log do servidor ou em " +
			"UPWATCH_SETUP_TOKEN"
		resp.Error.Field = "setup_token"
		writeJSON(w, http.StatusUnauthorized, resp)
		return
	}

	u, err := a.auth.CreateInitialAdmin(r.Context(), body.Username, body.Password)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// handleLogin confere a senha e abre a sessão, ou, se a conta tiver o
// segundo fator, devolve o desafio que /auth/login/2fa troca pela sessão.
//
// 200 nos dois casos: a senha estava certa, e o status diz o que falta.
// Senha errada é 401 igual com ou sem segundo fator.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if !decodeJSON(w, r, &body) {
		return
	}

	res, err := a.auth.Login(r.Context(), body.Username, body.Password, a.origins.origin(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			a.auditAs(r, body.Username, audit.AçãoLoginSenhaErrada, "/auth/login")
		case errors.Is(err, auth.ErrTooManyAttempts):
			a.auditLoginBlock(r, body.Username, "/auth/login", err)
		}
		writeStoreError(w, err)
		return
	}

	if res.NeedsSecondFactor() {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     "two_factor_required",
			"challenge":  res.Challenge,
			"expires_at": res.ChallengeExpiresAt,
		})
		return
	}
	a.startSession(w, res.Session)
}

// handleLoginSecondFactor troca desafio e código pela sessão.
func (a *API) handleLoginSecondFactor(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	entrou, err := a.auth.CompleteLogin(r.Context(), body.Challenge, body.Code, a.origins.origin(r))
	if err != nil {
		if errors.Is(err, auth.ErrTooManyAttempts) {
			a.auditLoginBlock(r, "", "/auth/login/2fa", err)
		}
		var código *auth.InvalidCodeError
		if errors.As(err, &código) {
			a.auditAs(r, código.Username, audit.AçãoLoginCódigoErrado, "/auth/login/2fa")
		}
		writeStoreError(w, err)
		return
	}

	// Login certo não gera linha de propósito: registrar todos encheria o
	// log e esconderia o que importa. Entrar por código de recuperação é
	// outra coisa — é passar por cima do aplicativo autenticador, que é o
	// que faz quem perdeu o celular e também quem roubou a lista. Sem
	// esta linha, o contorno do segundo fator seria o único evento de
	// segurança do sistema a não deixar rastro.
	if entrou.UsedRecoveryCode {
		a.auditAs(r, entrou.Username, audit.Ação2FARecuperaçãoUsada, entrou.Username)
	}
	a.startSession(w, entrou.Session)
}

// startSession entrega o cookie da sessão recém-aberta.
func (a *API) startSession(w http.ResponseWriter, secret string) {
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

// handleMe devolve a conta autenticada, e se ela precisa cadastrar o
// segundo fator antes de usar o resto da API. A interface lê isso aqui,
// e não esperando um 403, para levar direto ao cadastro.
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	u := mustUser(r)
	writeJSON(w, http.StatusOK, meResponse{User: u, MustEnroll: a.mustEnroll(r)})
}

// meResponse é a conta com o que só vale para quem pergunta.
type meResponse struct {
	domain.User
	MustEnroll bool
}

// MarshalJSON acrescenta o campo ao da conta. Sem isto, o MarshalJSON de
// domain.User, promovido pelo campo embutido, engoliria o campo novo.
func (m meResponse) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(m.User)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(base, &out); err != nil {
		return nil, err
	}
	out["two_factor_enrollment_required"] = json.RawMessage(strconv.FormatBool(m.MustEnroll))
	return json.Marshal(out)
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

	u := mustUser(r)
	if err := a.auth.ChangePassword(r.Context(), u.ID, body.Current, body.Next, a.origins.origin(r)); err != nil {
		// Quem está com um cookie roubado e tenta adivinhar a senha atual
		// aqui não gerava linha nenhuma: a rota de login registra o
		// palpite, esta não registrava. E o 429 por tentativas só era
		// registrado no login, então uma conta travada aparecia no log sem
		// causa.
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			a.audit(r, audit.AçãoSenhaAtualErrada, u.Username)
		case errors.Is(err, auth.ErrTooManyAttempts):
			a.auditLoginBlock(r, u.Username, u.Username, err)
		}
		writeStoreError(w, err)
		return
	}
	a.audit(r, audit.AçãoSenhaTrocada, u.Username)

	// A própria sessão desta requisição foi encerrada junto; apagar o
	// cookie evita o cliente insistir com uma credencial já inválida.
	http.SetCookie(w, a.sessionCookie("", time.Unix(0, 0)))
	w.WriteHeader(http.StatusNoContent)
}

// handleListTokens lista as credenciais programáticas da conta e anuncia a
// política de validade da instalação.
//
// A política vai junto da listagem para a interface montar o seletor de
// validade com só o que a emissão aceita: sem a opção "sem expiração"
// quando a instalação a exige, e sem prazo acima do teto. A barreira de
// verdade continua no servidor (ver IssueToken); isto é conveniência.
func (a *API) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := a.auth.ListTokens(r.Context(), mustUser(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if tokens == nil {
		tokens = []domain.APIToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  tokens,
		"policy": tokenPolicyBody(a.auth.TokenExpiryPolicy()),
	})
}

// tokenPolicyBody traduz a política para a resposta. O teto vai em
// segundos (0 = sem teto), que é o que a interface converte em datas.
func tokenPolicyBody(p domain.TokenExpiryPolicy) map[string]any {
	return map[string]any{
		"require_expiry":       p.Require,
		"max_lifetime_seconds": int64(p.MaxLifetime.Seconds()),
	}
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

	a.audit(r, audit.AçãoTokenCriado, tok.Name)

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

// handleRevokeToken revoga uma credencial da própria conta (autosserviço).
// Qualquer papel pode: é o que uma conta rebaixada ainda faz com os
// próprios tokens. O registro é retido, e a auditoria marca o individual
// (quantidade 1) com o motivo manual.
func (a *API) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.auth.RevokeToken(r.Context(), mustUser(r).ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	a.auditFull(r, audit.Evento{
		Ação:       audit.AçãoTokenRevogado,
		Alvo:       strconv.FormatInt(id, 10),
		Motivo:     domain.TokenRevokeManual,
		Quantidade: 1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeUserTokens revoga em massa os tokens de uma conta. É a
// revogação administrativa: o admin sempre pode cortar as credenciais de
// uma conta, para uma conta rebaixada ou comprometida não deixar token
// órfão. A auditoria marca o massa (ação própria), com a quantidade e o
// motivo, e o alvo é a conta.
func (a *API) handleRevokeUserTokens(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	alvo, err := a.store.Users().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	n, err := a.auth.RevokeAccountTokens(r.Context(), id, domain.TokenRevokeManual, mustUser(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.auditFull(r, audit.Evento{
		Ação:       audit.AçãoTokenRevogadosEmMassa,
		Alvo:       alvo.Username,
		Motivo:     domain.TokenRevokeManual,
		Quantidade: int(n),
	})
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// handleRotateToken emite um substituto para um token da própria conta e
// devolve o segredo novo. O antigo segue válido na sobreposição até ser
// revogado. Esta é, como a criação, a única resposta em que o segredo
// aparece.
func (a *API) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var body struct {
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	tok, secret, err := a.auth.RotateToken(r.Context(), mustUser(r).ID, id, body.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	a.audit(r, audit.AçãoTokenRotacionado, strconv.FormatInt(id, 10))

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

// handleUpdateTokenExpiry edita a validade de um token da própria conta.
//
// Encurtar ou adicionar prazo é livre; esticar respeita o teto da
// instalação e deixa rastro na auditoria. A recusa da política chega como
// 400 apontando expires_at (ver IssueToken para o mesmo caminho).
func (a *API) handleUpdateTokenExpiry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var body struct {
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	esticou, err := a.auth.UpdateTokenExpiry(r.Context(), mustUser(r).ID, id, body.ExpiresAt)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Só o esticão deixa rastro: encurtar e adicionar são sempre mais
	// restritivos, e registrá-los só encheria o log do que não importa.
	if esticou {
		a.audit(r, audit.AçãoTokenValidadeEsticada, strconv.FormatInt(id, 10))
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
