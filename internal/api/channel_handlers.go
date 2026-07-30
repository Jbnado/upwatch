package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
)

// channelRequest é o corpo aceito ao criar ou atualizar um canal.
type channelRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Config é a configuração do notificador: url do destino, cabeçalhos,
	// modelo de mensagem.
	Config  json.RawMessage `json:"config"`
	Enabled *bool           `json:"enabled,omitempty"`
}

func (req channelRequest) toDomain() domain.Channel {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return domain.Channel{
		Name: req.Name, Type: req.Type, Config: req.Config, Enabled: enabled,
	}
}

// handleListChannelTypes informa quais canais existem, para a interface
// montar a escolha sem manter uma lista própria que sai de sincronia.
func (a *API) handleListChannelTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": notifier.Types()})
}

func (a *API) handleListChannels(w http.ResponseWriter, r *http.Request) {
	canais, err := a.store.Channels().List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if canais == nil {
		canais = []domain.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": canais})
}

// handleCreateChannel cadastra um destino de aviso.
//
// A configuração é validada montando o notificador de verdade: descobrir
// que a url está errada só durante um incidente seria descobrir tarde
// demais, e é justamente quando a mensagem não pode falhar.
func (a *API) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	c := req.toDomain()
	if !validateChannel(w, c) {
		return
	}

	if err := a.store.Channels().Create(r.Context(), &c); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *API) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	c, err := a.store.Channels().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req channelRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	c := req.toDomain()
	c.ID = id
	if !validateChannel(w, c) {
		return
	}

	if err := a.store.Channels().Update(r.Context(), c); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.store.Channels().Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestChannel entrega um aviso de exemplo.
//
// Sem isto, a única forma de descobrir que o canal funciona seria esperar
// uma queda de verdade — e o momento de descobrir que o alerta não chega
// não pode ser esse.
func (a *API) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	c, err := a.store.Channels().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	destino, err := notifier.Build(c.Type, c.Config)
	if err != nil {
		writeFieldError(w, "config", err.Error())
		return
	}

	exemplo := notifier.Notification{
		Monitor: domain.Monitor{Name: "teste do UpWatch", Target: c.Name},
		Event: domain.StateChange{
			Kind: domain.ChangeUp, From: domain.StatusDown, To: domain.StatusUp,
			At: a.clock.Now(), Duration: 5 * time.Minute,
		},
	}

	// Entrega síncrona de propósito: quem apertou o botão está esperando
	// a resposta, e enfileirar devolveria "ok" sem saber se chegou.
	if err := destino.Send(r.Context(), exemplo); err != nil {
		writeError(w, http.StatusBadGateway, codeInvalidRequest,
			"o canal não aceitou a mensagem: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "entregue"})
}

// ---------- vínculo com monitores ----------

func (a *API) handleListMonitorChannels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	canais, err := a.store.Channels().ForMonitor(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if canais == nil {
		canais = []domain.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": canais})
}

func (a *API) handleLinkChannel(w http.ResponseWriter, r *http.Request) {
	monitorID, ok := pathID(w, r)
	if !ok {
		return
	}
	channelID, ok := pathParamID(w, r, "channelID")
	if !ok {
		return
	}

	if err := a.store.Channels().Link(r.Context(), monitorID, channelID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleUnlinkChannel(w http.ResponseWriter, r *http.Request) {
	monitorID, ok := pathID(w, r)
	if !ok {
		return
	}
	channelID, ok := pathParamID(w, r, "channelID")
	if !ok {
		return
	}

	if err := a.store.Channels().Unlink(r.Context(), monitorID, channelID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- incidentes ----------

type incidentResponse struct {
	ID              int64      `json:"id"`
	MonitorID       int64      `json:"monitor_id"`
	StartedAt       time.Time  `json:"started_at"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	DurationSeconds int        `json:"duration_seconds"`
	Cause           string     `json:"cause,omitempty"`
	Open            bool       `json:"open"`
}

// handleListIncidents devolve o histórico de quedas.
//
// A duração vem calculada: é a primeira pergunta sobre qualquer
// incidente, e obrigar o cliente a subtrair dois horários faria cada
// integração repetir a mesma conta.
func (a *API) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	filter := store.IncidentFilter{
		Page: store.PageFilter{
			AfterID: queryInt64(r, "after_id", 0),
			Limit:   int(queryInt64(r, "limit", 0)),
		},
		MonitorID: queryInt64(r, "monitor_id", 0),
		OnlyOpen:  r.URL.Query().Get("open") == "true",
	}

	page, err := a.store.Incidents().List(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	agora := a.clock.Now()
	items := make([]incidentResponse, 0, len(page.Items))
	for _, i := range page.Items {
		items = append(items, incidentResponse{
			ID: i.ID, MonitorID: i.MonitorID,
			StartedAt: i.StartedAt, ResolvedAt: i.ResolvedAt,
			DurationSeconds: int(i.Duration(agora).Seconds()),
			Cause:           i.Cause, Open: i.Open(),
		})
	}

	body := map[string]any{"items": items, "has_more": page.HasMore}
	if page.HasMore && len(page.Items) > 0 {
		body["next_after_id"] = page.Items[len(page.Items)-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

// ---------- auxiliares ----------

// validateChannel confere o domínio e a configuração do notificador.
func validateChannel(w http.ResponseWriter, c domain.Channel) bool {
	if err := c.Validate(); err != nil {
		writeStoreError(w, err)
		return false
	}
	if _, err := notifier.Build(c.Type, c.Config); err != nil {
		writeFieldError(w, "config", err.Error())
		return false
	}
	return true
}

// pathParamID lê um identificador nomeado da rota.
func pathParamID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		writeFieldError(w, name, "identificador inválido")
		return 0, false
	}
	return id, true
}
