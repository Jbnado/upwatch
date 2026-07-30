package api

import (
	"net/http"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Contas e papéis.
//
// A separação é por verbo, não por tela: tudo que altera o estado do
// sistema exige administrador, e tudo que só lê aceita observador. Uma
// permissão que vivesse apenas na interface — botão escondido, tela que
// não aparece — não seria permissão nenhuma, porque a mesma chamada sai
// de um curl com o token na mão.

type userRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	contas, err := a.store.Users().List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if contas == nil {
		contas = []domain.User{}
	}
	// domain.User.MarshalJSON já omite o hash; a listagem mostra quem
	// existe, não material para ataque offline.
	writeJSON(w, http.StatusOK, map[string]any{"items": contas})
}

func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	role, err := domain.ParseRole(req.Role)
	if err != nil {
		writeFieldError(w, "role", "papel desconhecido; use admin ou viewer")
		return
	}

	u, err := a.auth.CreateUser(r.Context(), req.Username, req.Password, role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

type roleRequest struct {
	Role string `json:"role"`
}

// handleUpdateUserRole troca o papel de uma conta.
//
// Só o papel: nome de usuário e senha não se editam por aqui. Trocar a
// senha de outra pessoa sem ela saber é o tipo de poder que uma
// ferramenta de monitoramento não precisa ter, e renomear conta quebra
// o rastro de quem fez o quê.
func (a *API) handleUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req roleRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	role, err := domain.ParseRole(req.Role)
	if err != nil {
		writeFieldError(w, "role", "papel desconhecido; use admin ou viewer")
		return
	}

	u, err := a.auth.SetRole(r.Context(), id, role)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.auth.DeleteUser(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireAdmin barra quem não pode escrever.
//
// Middleware, e não uma checagem em cada handler: esquecer a checagem
// num handler novo é o modo mais provável de a permissão vazar, e aqui o
// esquecimento é impossível — a rota inteira mora dentro do grupo.
func (a *API) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := mustUser(r)
		if !u.Role.CanWrite() {
			writeError(w, http.StatusForbidden, codeForbidden,
				"esta conta só tem permissão de leitura")
			return
		}
		next.ServeHTTP(w, r)
	})
}
