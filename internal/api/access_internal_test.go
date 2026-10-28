package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Rota sem política de escopo.
//
// A guarda classificava a rota pelo prefixo do caminho e deixava passar o
// que não reconhecia. Uma rota nova com monitor no caminho, mas num
// prefixo que ninguém previu, nascia aberta — e "falhar aberto" em acesso
// é vazamento esperando alguém criar a rota certa. Agora cada rota declara
// a política ao ser registrada, e a que não declara não é atendida.

// A guarda recusa a rota que não declarou política, sem chamar o handler.
// Responder 500 e não 404: é defeito de quem escreveu a rota, e precisa
// aparecer no primeiro teste, não parecer um recurso que não existe.
func TestScopeGuardFailsClosedForRouteWithoutPolicy(t *testing.T) {
	a := &API{policies: map[string]scopePolicy{}}

	chamou := false
	r := chi.NewRouter()
	r.With(a.scopeGuard).Get("/api/v1/monitors-novos/{id}", func(w http.ResponseWriter, _ *http.Request) {
		chamou = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/monitors-novos/1", nil))

	if chamou {
		t.Fatal("a guarda deixou passar uma rota sem política de escopo")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// O roteador nem chega a subir com uma rota sem política: o defeito
// aparece ao montar, antes de qualquer requisição.
func TestCheckPoliciesRefusesUndeclaredRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/api/v1/declarada", func(http.ResponseWriter, *http.Request) {})
	r.Get("/api/v1/esquecida/{id}", func(http.ResponseWriter, *http.Request) {})

	err := checkPolicies(r, map[string]scopePolicy{"GET /api/v1/declarada": policyAccount})
	if err == nil || !strings.Contains(err.Error(), "GET /api/v1/esquecida/{id}") {
		t.Fatalf("checkPolicies = %v, want erro citando a rota esquecida", err)
	}

	if err := checkPolicies(r, map[string]scopePolicy{
		"GET /api/v1/declarada":      policyAccount,
		"GET /api/v1/esquecida/{id}": policyMonitor,
	}); err != nil {
		t.Fatalf("checkPolicies com tudo declarado = %v", err)
	}
}
