package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// monitorRequest é o corpo aceito ao criar ou atualizar um monitor.
//
// Tipo próprio em vez de domain.Monitor: expor a struct interna deixaria o
// cliente enviar id e timestamps, e amarraria o formato público a
// mudanças internas.
type monitorRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`

	Target string `json:"target"`

	// Intervalos em segundos, que é como um operador pensa — nanossegundos
	// do Go seriam um detalhe de implementação vazando para a API.
	IntervalSeconds int `json:"interval_seconds"`
	TimeoutSeconds  int `json:"timeout_seconds"`

	ConfirmationThreshold int `json:"confirmation_threshold,omitempty"`
	DegradedLatencyMillis int `json:"degraded_latency_ms,omitempty"`

	Config   json.RawMessage `json:"config,omitempty"`
	ParentID *int64          `json:"parent_id,omitempty"`
	Enabled  *bool           `json:"enabled,omitempty"`
	Tags     []string        `json:"tags,omitempty"`
}

// toDomain converte o corpo recebido, aplicando os padrões.
func (req monitorRequest) toDomain() (domain.Monitor, error) {
	typ, err := domain.ParseMonitorType(req.Type)
	if err != nil {
		return domain.Monitor{}, &domain.ValidationError{
			Field: "type", Msg: "tipo de monitor desconhecido",
		}
	}

	threshold := req.ConfirmationThreshold
	if threshold == 0 {
		threshold = 3
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	return domain.Monitor{
		Name:                  req.Name,
		Type:                  typ,
		Target:                req.Target,
		Interval:              time.Duration(req.IntervalSeconds) * time.Second,
		Timeout:               time.Duration(req.TimeoutSeconds) * time.Second,
		ConfirmationThreshold: threshold,
		DegradedLatency:       time.Duration(req.DegradedLatencyMillis) * time.Millisecond,
		Config:                req.Config,
		ParentID:              req.ParentID,
		Enabled:               enabled,
		// Normalizada na entrada, não na leitura: "Produção" e "produção"
		// digitadas em momentos diferentes viram o mesmo grupo, e quem
		// consome a API recebe sempre a forma canônica.
		Tags: domain.NormalizeTags(req.Tags),
	}, nil
}

// monitorResponse é o formato devolvido.
type monitorResponse struct {
	ID                    int64           `json:"id"`
	Name                  string          `json:"name"`
	Type                  string          `json:"type"`
	Target                string          `json:"target"`
	IntervalSeconds       int             `json:"interval_seconds"`
	TimeoutSeconds        int             `json:"timeout_seconds"`
	ConfirmationThreshold int             `json:"confirmation_threshold"`
	DegradedLatencyMillis int             `json:"degraded_latency_ms"`
	Config                json.RawMessage `json:"config,omitempty"`
	ParentID              *int64          `json:"parent_id,omitempty"`
	Enabled               bool            `json:"enabled"`
	Tags                  []string        `json:"tags"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

func toMonitorResponse(m domain.Monitor) monitorResponse {
	tags := m.Tags
	if tags == nil {
		// Lista vazia em vez de null: o cliente pode iterar sem checar.
		tags = []string{}
	}
	return monitorResponse{
		ID: m.ID, Name: m.Name, Type: m.Type.String(), Target: m.Target,
		IntervalSeconds:       int(m.Interval.Seconds()),
		TimeoutSeconds:        int(m.Timeout.Seconds()),
		ConfirmationThreshold: m.ConfirmationThreshold,
		DegradedLatencyMillis: int(m.DegradedLatency.Milliseconds()),
		Config:                m.Config, ParentID: m.ParentID,
		Enabled: m.Enabled, Tags: tags,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// handleListMonitors devolve uma página de monitores.
func (a *API) handleListMonitors(w http.ResponseWriter, r *http.Request) {
	filter := store.MonitorFilter{
		Page: store.PageFilter{
			AfterID: queryInt64(r, "after_id", 0),
			Limit:   int(queryInt64(r, "limit", 0)),
		},
		Tag: r.URL.Query().Get("tag"),
	}
	if raw := r.URL.Query().Get("enabled"); raw != "" {
		enabled := raw == "true"
		filter.Enabled = &enabled
	}

	page, err := a.store.Monitors().List(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	items := make([]monitorResponse, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, toMonitorResponse(m))
	}

	body := map[string]any{"items": items, "has_more": page.HasMore}
	// O cursor da próxima página vem pronto, para o cliente não precisar
	// deduzir como paginar.
	if page.HasMore && len(page.Items) > 0 {
		body["next_after_id"] = page.Items[len(page.Items)-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

// handleCreateMonitor cadastra um monitor.
func (a *API) handleCreateMonitor(w http.ResponseWriter, r *http.Request) {
	var req monitorRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	m, err := req.toDomain()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !a.validateMonitor(w, &m) {
		return
	}

	if err := a.store.Monitors().Create(r.Context(), &m); err != nil {
		writeStoreError(w, err)
		return
	}

	a.notifyScheduler(m)
	a.events.publish(event{Type: "monitor.created", Data: toMonitorResponse(m)})
	writeJSON(w, http.StatusCreated, toMonitorResponse(m))
}

// handleGetMonitor devolve um monitor.
func (a *API) handleGetMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	m, err := a.store.Monitors().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMonitorResponse(m))
}

// handleUpdateMonitor sobrescreve um monitor.
func (a *API) handleUpdateMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req monitorRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	m, err := req.toDomain()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.ID = id
	if !a.validateMonitor(w, &m) {
		return
	}

	if err := a.store.Monitors().Update(r.Context(), m); err != nil {
		writeStoreError(w, err)
		return
	}

	a.notifyScheduler(m)
	a.events.publish(event{Type: "monitor.updated", Data: toMonitorResponse(m)})
	writeJSON(w, http.StatusOK, toMonitorResponse(m))
}

// handleDeleteMonitor remove um monitor e seu histórico.
func (a *API) handleDeleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.store.Monitors().Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}

	if a.scheduler != nil {
		a.scheduler.Remove(id)
	}
	a.events.publish(event{Type: "monitor.deleted", Data: map[string]int64{"id": id}})
	w.WriteHeader(http.StatusNoContent)
}

// validateMonitor aplica as invariantes do domínio e as do checker.
//
// A validação específica do tipo acontece no cadastro, não na primeira
// execução: descobrir uma regex inválida só quando o monitor rodar
// deixaria um alvo sem vigilância sem que nada avisasse.
func (a *API) validateMonitor(w http.ResponseWriter, m *domain.Monitor) bool {
	if err := m.Validate(); err != nil {
		writeStoreError(w, err)
		return false
	}
	if a.checkers == nil {
		return true
	}
	if err := a.checkers.Validate(*m); err != nil {
		writeFieldError(w, "config", err.Error())
		return false
	}
	return true
}

// notifyScheduler avisa o agendador da mudança, para o monitor passar a
// ser verificado sem esperar reinício.
func (a *API) notifyScheduler(m domain.Monitor) {
	if a.scheduler != nil {
		a.scheduler.Upsert(m)
	}
}

// queryInt64 lê um parâmetro numérico da URL, com padrão.
func queryInt64(r *http.Request, name string, def int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}
