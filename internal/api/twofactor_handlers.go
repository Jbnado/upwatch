package api

import (
	"log/slog"
	"net/http"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Verificação em duas etapas da própria conta.
//
// Tudo aqui é da conta que pede, nunca de outra: o {id} não aparece nas
// rotas, e o desligamento por um administrador mora em /users/{id}/2fa,
// sob as regras de quem pode mexer em qual conta.

// sessionSecret é o segredo da sessão da requisição, vazio quando ela
// veio por token de API. É a sessão que fica quando as outras caem.
func sessionSecret(r *http.Request) string {
	if _, ok := bearerToken(r); ok {
		return ""
	}
	if c, err := r.Cookie(SessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// handleTOTPStatus informa se o fator está ligado e quantos códigos de
// recuperação sobram.
func (a *API) handleTOTPStatus(w http.ResponseWriter, r *http.Request) {
	st, err := a.auth.TOTPStatus(r.Context(), mustUser(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":             st.Enabled,
		"pending":             st.Pending,
		"recovery_codes_left": st.RecoveryCodesLeft,
		"required":            a.require2FAAdmin && mustUser(r).Role == domain.RoleAdmin,
	})
}

// handleTOTPSetup começa o cadastro: o segredo e o endereço do QR code.
//
// É a única resposta em que o segredo aparece. Chamar de novo sorteia
// outro e descarta o anterior, que nunca chegou a valer.
func (a *API) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	e, err := a.auth.BeginTOTP(r.Context(), mustUser(r).ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"secret": e.Secret, "uri": e.URI})
}

// handleTOTPEnable confirma o cadastro com um código do aplicativo.
func (a *API) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}

	u := mustUser(r)
	codes, err := a.auth.ConfirmTOTP(r.Context(), u.ID, sessionSecret(r), body.Code)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	slog.Info("verificação em duas etapas ligada", "conta", u.Username)
	writeRecoveryCodes(w, codes)
}

// proofRequest é o que desligar e trocar os códigos exigem: a senha e um
// código, do aplicativo ou de recuperação.
type proofRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// handleTOTPRecoveryCodes troca os códigos de recuperação.
func (a *API) handleTOTPRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var body proofRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	u := mustUser(r)
	codes, err := a.auth.RegenerateRecoveryCodes(r.Context(), u.ID, body.Password, body.Code)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	slog.Info("códigos de recuperação trocados", "conta", u.Username)
	writeRecoveryCodes(w, codes)
}

// handleTOTPDisable desliga o fator da própria conta.
func (a *API) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	var body proofRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	u := mustUser(r)
	if err := a.auth.DisableTOTP(r.Context(), u.ID, sessionSecret(r), body.Password, body.Code); err != nil {
		writeStoreError(w, err)
		return
	}
	slog.Info("verificação em duas etapas desligada", "conta", u.Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleResetUserTOTP desliga o fator de outra conta, para quem perdeu o
// celular. Fica no log com quem fez: é o caminho que contorna o segundo
// fator, e precisa deixar rastro.
func (a *API) handleResetUserTOTP(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	actor := mustUser(r)
	target, err := a.store.Users().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.auth.ResetTOTP(r.Context(), actor.ID, id); err != nil {
		writeStoreError(w, err)
		return
	}
	slog.Warn("verificação em duas etapas desligada por administrador",
		"administrador", actor.Username, "conta", target.Username, "conta_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// writeRecoveryCodes devolve os códigos, que não aparecem de novo.
func writeRecoveryCodes(w http.ResponseWriter, codes []string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"recovery_codes": codes,
		"warning":        "guarde estes códigos agora; eles não podem ser mostrados de novo",
	})
}
