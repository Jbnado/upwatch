package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// capturaLogPadrão troca o logger do processo, que é onde o recoverPanic
// escreve, e devolve o buffer com o que o operador leria.
func capturaLogPadrão(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	anterior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(anterior) })
	return &buf
}

// O pânico é registrado para alguém investigar, e investigar precisa do
// caminho. Mas o caminho do push carrega o segredo do monitor, então o
// registro do defeito não pode ser a porta dos fundos por onde o token sai
// da máquina — é o mesmo vazamento da linha de auditoria, em outro fluxo.
//
// O padrão da rota serve para investigar igual: diz qual endpoint quebrou,
// sem dizer com que credencial.
func TestRecoverPanicLogsTheRoutePatternNotThePath(t *testing.T) {
	buf := capturaLogPadrão(t)

	const segredo = "token-do-cron-que-nao-pode-vazar"
	r := chi.NewRouter()
	r.Use(recoverPanic)
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/push/{token}", func(http.ResponseWriter, *http.Request) {
			panic("defeito proposital no handler")
		})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/push/"+segredo, nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	log := buf.String()
	if !strings.Contains(log, "pânico no handler") {
		t.Fatalf("o pânico não foi registrado:\n%s", log)
	}
	if strings.Contains(log, segredo) {
		t.Errorf("o log do pânico trouxe o token do caminho:\n%s", log)
	}
	// Sem o endpoint no registro, o pânico vira um erro sem endereço e
	// ninguém sabe o que investigar.
	if !strings.Contains(log, "/push/{token}") {
		t.Errorf("o log do pânico não diz qual rota quebrou:\n%s", log)
	}
}
