package api_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// A configuração HTTP é regravada pela struct do checker (ver
// checker.CanonicalConfig), e o que não estiver nela é descartado sem
// erro. Um campo novo esquecido na struct sumiria na primeira edição, de
// um jeito que nenhum teste do checker veria: a criação devolve o que
// recebeu, e é só o formulário reenviando o GET que perde as asserções.
// O caminho inteiro é o que a interface faz: criar, ler, salvar sem mexer
// e ler de novo. Com basic auth e cabeçalho de credencial, para passar
// também pela preservação de segredos, que reescreve a configuração.
func TestHTTPJSONAssertionsSurviveAnUntouchedEdit(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	assertions := []any{
		map[string]any{"path": "$.status", "op": "eq", "value": "ok"},
		map[string]any{"path": "$.queue_lag", "op": "lt", "value": 100},
		map[string]any{"path": "$.erro", "op": "eq", "value": nil},
		map[string]any{"path": `$["fila de saída"][0].workers`, "op": "gte", "value": 1.5},
		map[string]any{"path": "$.versao", "op": "exists"},
	}
	body := httpMonitorBody("https://api.exemplo.com/saude")
	body["config"].(map[string]any)["json_assertions"] = assertions
	id := s.createMonitor(t, body)

	get := func(t *testing.T) map[string]any {
		t.Helper()
		resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
		assertStatus(t, resp, http.StatusOK)
		return decode[map[string]any](t, resp)
	}
	want := normalizeJSON(t, assertions)

	first := get(t)
	cfg, _ := first["config"].(map[string]any)
	if got := normalizeJSON(t, cfg["json_assertions"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("json_assertions after create = %v, want %v", got, want)
	}

	// O corpo da edição é o que o formulário monta a partir do GET.
	edit := map[string]any{}
	for _, k := range []string{"name", "type", "target", "interval_seconds", "timeout_seconds",
		"confirmation_threshold", "degraded_latency_ms", "config", "tags", "enabled"} {
		edit[k] = first[k]
	}
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), edit)
	assertStatus(t, resp, http.StatusOK)
	updated := decode[map[string]any](t, resp)
	if got := normalizeJSON(t, updated["config"].(map[string]any)["json_assertions"]); !reflect.DeepEqual(got, want) {
		t.Errorf("json_assertions in the edit response = %v, want %v", got, want)
	}

	second := get(t)
	cfg, _ = second["config"].(map[string]any)
	if got := normalizeJSON(t, cfg["json_assertions"]); !reflect.DeepEqual(got, want) {
		t.Errorf("json_assertions after an untouched edit = %v, want %v", got, want)
	}
	// O que mais passa pela mesma reescrita também volta inteiro.
	if !reflect.DeepEqual(second["config"], first["config"]) {
		t.Errorf("config after an untouched edit = %v, want %v", second["config"], first["config"])
	}
}

// normalizeJSON passa o valor por um ciclo de JSON, para a comparação não
// depender de int contra float64.
func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
