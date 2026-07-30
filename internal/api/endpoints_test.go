package api_test

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// newMonitorBody devolve um corpo válido de criação de monitor.
func newMonitorBody(name string) map[string]any {
	return map[string]any{
		"name":             name,
		"type":             "http",
		"target":           "https://example.com/health",
		"interval_seconds": 60,
		"timeout_seconds":  10,
	}
}

// createMonitor cadastra um monitor e devolve seu id.
func (s *server) createMonitor(t *testing.T, body map[string]any) int64 {
	t.Helper()

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)
	assertStatus(t, resp, http.StatusCreated)

	got := decode[map[string]any](t, resp)
	return int64(got["id"].(float64))
}

// ---------- CRUD de monitor ----------

func TestCreateMonitorReturnsTheStoredRepresentation(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", newMonitorBody("api"))
	assertStatus(t, resp, http.StatusCreated)

	got := decode[map[string]any](t, resp)
	if got["name"] != "api" {
		t.Errorf("name = %v, want %q", got["name"], "api")
	}
	if got["type"] != "http" {
		t.Errorf("type = %v, want %q", got["type"], "http")
	}
	// Intervalos saem em segundos: nanossegundos do Go seriam detalhe de
	// implementação vazando para a API.
	if got["interval_seconds"] != float64(60) {
		t.Errorf("interval_seconds = %v, want 60", got["interval_seconds"])
	}
	if got["id"] == nil || got["id"].(float64) == 0 {
		t.Error("the response carries no generated id")
	}
}

func TestCreateMonitorRejectsInvalidInterval(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	body := newMonitorBody("api")
	body["interval_seconds"] = 1 // abaixo do mínimo

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)
	assertStatus(t, resp, http.StatusBadRequest)

	got := decode[map[string]any](t, resp)
	errObj := got["error"].(map[string]any)
	if errObj["field"] != "interval" {
		t.Errorf("error field = %v, want %q", errObj["field"], "interval")
	}
}

// Timeout maior que o intervalo faz os checks se acumularem; recusar no
// cadastro evita um monitor que satura o pool desde o primeiro minuto.
func TestCreateMonitorRejectsTimeoutAboveInterval(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	body := newMonitorBody("api")
	body["timeout_seconds"] = 120

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)

	assertStatus(t, resp, http.StatusBadRequest)
}

// A configuração específica do tipo é validada no cadastro: descobrir uma
// regex inválida só na primeira execução deixaria o alvo sem vigilância
// sem que nada avisasse.
func TestCreateMonitorRejectsInvalidCheckerConfig(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	body := newMonitorBody("api")
	body["config"] = map[string]any{"body_regex": "([desbalanceado"}

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)
	assertStatus(t, resp, http.StatusBadRequest)

	got := decode[map[string]any](t, resp)
	if got["error"].(map[string]any)["field"] != "config" {
		t.Errorf("error field = %v, want %q", got["error"], "config")
	}
}

func TestCreateMonitorRejectsDuplicateName(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", newMonitorBody("api"))

	assertStatus(t, resp, http.StatusConflict)
}

// Campo desconhecido vira erro explícito em vez de ser ignorado: um erro
// de digitação passaria por configuração aplicada.
func TestCreateMonitorRejectsUnknownField(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	body := newMonitorBody("api")
	body["intervalo"] = 60 // grafia errada

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)

	assertStatus(t, resp, http.StatusBadRequest)
}

func TestGetMonitorReturnsIt(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusOK)

	got := decode[map[string]any](t, resp)
	if got["name"] != "api" {
		t.Errorf("name = %v, want %q", got["name"], "api")
	}
}

func TestUpdateMonitorPersistsChanges(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	body := newMonitorBody("api renomeada")
	body["interval_seconds"] = 300
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), body)
	assertStatus(t, resp, http.StatusOK)

	got := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, got, http.StatusOK)

	decoded := decode[map[string]any](t, got)
	if decoded["name"] != "api renomeada" {
		t.Errorf("name = %v, want %q", decoded["name"], "api renomeada")
	}
	if decoded["interval_seconds"] != float64(300) {
		t.Errorf("interval_seconds = %v, want 300", decoded["interval_seconds"])
	}
}

func TestDeleteMonitorRemovesIt(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodDelete, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusNoContent)

	after := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, after, http.StatusNotFound)
}

func TestDeleteMissingMonitorIsNotFound(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodDelete, "/api/v1/monitors/9999", nil)

	assertStatus(t, resp, http.StatusNotFound)
}

func TestMonitorIDMustBeNumeric(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/abc", nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

// ---------- listagem e paginação ----------

// A listagem entrega o cursor pronto, para o cliente não precisar deduzir
// como paginar.
func TestListMonitorsPaginates(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	for i := 0; i < 5; i++ {
		s.createMonitor(t, newMonitorBody("monitor-"+itoa(int64(i))))
	}

	resp := s.do(t, http.MethodGet, "/api/v1/monitors?limit=2", nil)
	assertStatus(t, resp, http.StatusOK)

	got := decode[map[string]any](t, resp)
	items := got["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("returned %d items, want 2", len(items))
	}
	if got["has_more"] != true {
		t.Error("has_more = false with more monitors to come, want true")
	}
	if got["next_after_id"] == nil {
		t.Error("the response carries no cursor for the next page")
	}
}

func TestListMonitorsFiltersByEnabled(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, newMonitorBody("ativo"))

	paused := newMonitorBody("pausado")
	paused["enabled"] = false
	s.createMonitor(t, paused)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors?enabled=true", nil)
	assertStatus(t, resp, http.StatusOK)

	items := decode[map[string]any](t, resp)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("returned %d items, want 1", len(items))
	}
	if items[0].(map[string]any)["name"] != "ativo" {
		t.Errorf("returned %v, want the enabled monitor", items[0])
	}
}

// Lista vazia devolve array, não null: o cliente itera sem precisar
// verificar.
func TestListMonitorsReturnsEmptyArrayNotNull(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors", nil)
	assertStatus(t, resp, http.StatusOK)

	body := readBody(t, resp)
	if strings.Contains(body, `"items":null`) {
		t.Errorf("empty listing returned null instead of an array: %s", body)
	}
}

// ---------- dados ----------

// seedHeartbeats grava batidas diretamente, simulando o que o agendador
// teria produzido.
func (s *server) seedHeartbeats(t *testing.T, monitorID int64, n int) {
	t.Helper()

	batch := make([]domain.Heartbeat, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, domain.Heartbeat{
			MonitorID: monitorID,
			Timestamp: epoch.Add(-time.Duration(n-i) * time.Minute),
			Status:    domain.StatusUp,
			LatencyMS: int64(100 + i),
		})
	}
	if err := s.store.WriteHeartbeats(context.Background(), batch); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}
}

func TestHeartbeatsEndpointReturnsRecentSamples(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	s.seedHeartbeats(t, id, 10)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/heartbeats", nil)
	assertStatus(t, resp, http.StatusOK)

	items := decode[map[string]any](t, resp)["items"].([]any)
	if len(items) != 10 {
		t.Fatalf("returned %d heartbeats, want 10", len(items))
	}
	first := items[0].(map[string]any)
	if first["status"] != "up" {
		t.Errorf("status = %v, want %q", first["status"], "up")
	}
}

func TestHeartbeatsEndpointRejectsMalformedDate(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/heartbeats?from=ontem", nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

func TestHeartbeatsEndpointRejectsInvertedWindow(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	path := "/api/v1/monitors/" + itoa(id) + "/heartbeats" +
		"?from=" + epoch.Format(time.RFC3339) +
		"&to=" + epoch.Add(-time.Hour).Format(time.RFC3339)

	resp := s.do(t, http.MethodGet, path, nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

func TestRollupsEndpointReturnsAggregates(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	if err := s.store.WriteRollups(context.Background(), []domain.Rollup{{
		MonitorID: id, ProbeID: domain.DefaultProbeID,
		Resolution: domain.ResolutionHourly, BucketStart: epoch.Add(-time.Hour),
		Total: 60, Up: 57, Down: 3,
		LatencySamples: 57, LatencyP95MS: 480,
	}}); err != nil {
		t.Fatalf("WriteRollups returned unexpected error: %v", err)
	}

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/rollups", nil)
	assertStatus(t, resp, http.StatusOK)

	got := decode[map[string]any](t, resp)
	items := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("returned %d rollups, want 1", len(items))
	}

	first := items[0].(map[string]any)
	if first["uptime_percent"] != float64(95) {
		t.Errorf("uptime_percent = %v, want 95", first["uptime_percent"])
	}
	// Percentis são o motivo de o agregado existir; perdê-los na resposta
	// esvaziaria o recurso.
	if first["latency_p95_ms"] != float64(480) {
		t.Errorf("latency_p95_ms = %v, want 480", first["latency_p95_ms"])
	}
}

func TestRollupsEndpointRejectsUnknownResolution(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/rollups?resolution=semanal", nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

// ---------- exportação ----------

// Exportar é o que impede o UpWatch de virar um depósito do qual não se
// tira nada: quem instala precisa poder levar seus dados embora.
func TestExportProducesCSV(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	s.seedHeartbeats(t, id, 5)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/export?format=csv", nil)
	assertStatus(t, resp, http.StatusOK)

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}

	rows, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("the exported CSV is malformed: %v", err)
	}
	if len(rows) != 6 { // cabeçalho + 5 batidas
		t.Fatalf("exported %d rows, want 6", len(rows))
	}
	if rows[0][0] != "timestamp" {
		t.Errorf("first column is %q, want a header row", rows[0][0])
	}
}

func TestExportProducesJSON(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))
	s.seedHeartbeats(t, id, 3)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/export?format=json", nil)
	assertStatus(t, resp, http.StatusOK)

	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("the exported JSON is malformed: %v", err)
	}
	if len(got.Items) != 3 {
		t.Errorf("exported %d items, want 3", len(got.Items))
	}
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, newMonitorBody("api"))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/export?format=xml", nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

// ---------- push ----------

// pushMonitorBody monta um monitor push com o token dado.
func pushMonitorBody(name, token string) map[string]any {
	return map[string]any{
		"name":             name,
		"type":             "push",
		"interval_seconds": 60,
		"timeout_seconds":  10,
		"config":           map[string]any{"token": token},
	}
}

func TestPushRecordsTheSignal(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	const token = "um-token-suficientemente-longo"
	id := s.createMonitor(t, pushMonitorBody("cron noturno", token))

	// O endpoint de push não usa sessão: quem reporta é um cron.
	s.cookie = nil
	resp := s.do(t, http.MethodPost, "/api/v1/push/"+token, nil)
	assertStatus(t, resp, http.StatusOK)

	at, ok, err := s.store.LastPush(context.Background(), id)
	if err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("no push was recorded")
	}
	if !at.Equal(epoch) {
		t.Errorf("push recorded at %v, want %v", at, epoch)
	}
}

func TestPushWithWrongTokenIsUnauthorized(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, pushMonitorBody("cron noturno", "um-token-suficientemente-longo"))

	s.cookie = nil
	resp := s.do(t, http.MethodPost, "/api/v1/push/token-errado", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

// ---------- eventos ao vivo ----------

// A interface precisa refletir a mudança sem recarregar; sem isso o
// operador ficaria olhando uma tela desatualizada durante um incidente.
func TestEventStreamDeliversMonitorChanges(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.AddCookie(s.cookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("opening the stream returned unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	// Cria um monitor enquanto o fluxo está aberto.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.do(t, http.MethodPost, "/api/v1/monitors", newMonitorBody("api")) //nolint:errcheck
	}()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(5 * time.Second)
	for scanner.Scan() && time.Now().Before(deadline) {
		if strings.Contains(scanner.Text(), "monitor.created") {
			return
		}
	}
	t.Fatal("the stream never delivered the monitor.created event")
}

func TestEventStreamRequiresAuthentication(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	resp := s.do(t, http.MethodGet, "/api/v1/events", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

func TestMonitorTagsAreNormalized(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// O campo da interface separa por vírgula, e o que chega é o que a
	// pessoa digitou: caixa misturada, espaço sobrando, repetição.
	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "api", "type": "http", "target": "https://exemplo.com",
		"interval_seconds": 60, "timeout_seconds": 10,
		"tags": []string{"Produção", "  produção ", "API", ""},
	})
	assertStatus(t, resp, http.StatusCreated)

	criado := decode[struct {
		ID   int64    `json:"id"`
		Tags []string `json:"tags"`
	}](t, resp)

	if len(criado.Tags) != 2 {
		t.Fatalf("etiquetas não foram unificadas: %v", criado.Tags)
	}
	if criado.Tags[0] != "api" || criado.Tags[1] != "produção" {
		t.Fatalf("forma canônica divergiu: %v", criado.Tags)
	}
}

func TestMonitorListFiltersByTag(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	for nome, tag := range map[string]string{"api-prod": "produção", "api-homolog": "homolog"} {
		resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
			"name": nome, "type": "http", "target": "https://exemplo.com/" + nome,
			"interval_seconds": 60, "timeout_seconds": 10, "tags": []string{tag},
		})
		assertStatus(t, resp, http.StatusCreated)
	}

	resp := s.do(t, http.MethodGet, "/api/v1/monitors?tag=produ%C3%A7%C3%A3o", nil)
	assertStatus(t, resp, http.StatusOK)

	page := decode[struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}](t, resp)

	if len(page.Items) != 1 || page.Items[0].Name != "api-prod" {
		t.Fatalf("filtro por etiqueta divergiu: %+v", page.Items)
	}
}

func TestMonitorRejectsHostileTag(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "api", "type": "http", "target": "https://exemplo.com",
		"interval_seconds": 60, "timeout_seconds": 10,
		"tags": []string{`pro"d`},
	})

	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"tags"`) {
		t.Error("erro não apontou o campo tags")
	}
}
