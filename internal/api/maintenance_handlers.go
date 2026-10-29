package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Janelas de manutenção.
//
// Escrever exige escopo total, como os relatos: a janela cala avisos, e
// uma conta restrita que declarasse manutenção num grupo vizinho calaria
// o pager de outro time. Ler é de todos, recortado pelo escopo.

// MaintenanceReloader é avisado a cada janela gravada, para o motor
// passar a respeitá-la na batida seguinte, sem esperar a releitura
// periódica.
type MaintenanceReloader interface {
	Reload(ctx context.Context) error
}

type maintenanceRequest struct {
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	// Recurrence vazia é a janela avulsa.
	Recurrence string `json:"recurrence"`
	// TimeZone vazio é UTC.
	TimeZone   string  `json:"time_zone"`
	MonitorIDs []int64 `json:"monitor_ids"`
	GroupIDs   []int64 `json:"group_ids"`
}

// maintenanceResponse é a janela com o que o cliente precisaria da regra
// de recorrência para calcular: se está em curso e quando é a próxima.
type maintenanceResponse struct {
	domain.MaintenanceWindow
	State domain.MaintenanceState       `json:"state"`
	Next  *domain.MaintenanceOccurrence `json:"next,omitempty"`
}

func toMaintenanceResponse(w domain.MaintenanceWindow, agora time.Time) maintenanceResponse {
	if w.MonitorIDs == nil {
		w.MonitorIDs = []int64{}
	}
	if w.GroupIDs == nil {
		w.GroupIDs = []int64{}
	}
	resp := maintenanceResponse{MaintenanceWindow: w, State: w.StateAt(agora)}
	if o, ok := w.NextOccurrence(agora); ok {
		resp.Next = &o
	}
	return resp
}

func (req maintenanceRequest) toDomain() domain.MaintenanceWindow {
	rec := domain.Recurrence(req.Recurrence)
	if rec == "" {
		rec = domain.RecurrenceNone
	}
	tz := strings.TrimSpace(req.TimeZone)
	if tz == "" {
		tz = "UTC"
	}
	return domain.MaintenanceWindow{
		Title:      strings.TrimSpace(req.Title),
		Message:    strings.TrimSpace(req.Message),
		StartsAt:   req.StartsAt.UTC().Truncate(time.Millisecond),
		EndsAt:     req.EndsAt.UTC().Truncate(time.Millisecond),
		Recurrence: rec,
		TimeZone:   tz,
		MonitorIDs: req.MonitorIDs,
		GroupIDs:   req.GroupIDs,
	}
}

// maintenanceFromRequest valida o corpo e confere que os alvos existem,
// com a resposta apontando o campo — o 404 da chave estrangeira diria só
// "registro não encontrado".
func (a *API) maintenanceFromRequest(w http.ResponseWriter, r *http.Request) (domain.MaintenanceWindow, bool) {
	var req maintenanceRequest
	if !decodeJSON(w, r, &req) {
		return domain.MaintenanceWindow{}, false
	}
	mw := req.toDomain()
	if err := mw.Validate(); err != nil {
		writeStoreError(w, err)
		return domain.MaintenanceWindow{}, false
	}

	v := viewerFrom(r)
	for _, id := range mw.GroupIDs {
		if !v.access.Group(id) {
			writeFieldError(w, "group_ids", fmt.Sprintf("grupo %d não encontrado", id))
			return domain.MaintenanceWindow{}, false
		}
	}
	for _, id := range mw.MonitorIDs {
		if _, err := a.store.Monitors().Get(r.Context(), id); err != nil {
			writeFieldError(w, "monitor_ids", fmt.Sprintf("monitor %d não encontrado", id))
			return domain.MaintenanceWindow{}, false
		}
	}
	return mw, true
}

// reloadMaintenance avisa o motor. A falha não desfaz a gravação: a
// releitura periódica alcança a janela logo depois.
func (a *API) reloadMaintenance(ctx context.Context) {
	if a.maintenance == nil {
		return
	}
	if err := a.maintenance.Reload(ctx); err != nil {
		slog.Warn("falha ao recarregar as janelas de manutenção", "erro", err)
	}
}

// visibleMaintenance recorta a janela pelo escopo: some a que não toca
// nada visível, e da que sobra saem os alvos de fora. A lista de ids
// confirmaria monitores e grupos que a conta não alcança.
func (a *API) visibleMaintenance(r *http.Request, janelas []domain.MaintenanceWindow) ([]domain.MaintenanceWindow, error) {
	v := viewerFrom(r)
	visiveis, err := a.visibleMonitorIDs(r.Context(), v)
	if err != nil || visiveis == nil {
		return janelas, err
	}

	out := make([]domain.MaintenanceWindow, 0, len(janelas))
	for _, jw := range janelas {
		var monitores, grupos []int64
		for _, id := range jw.MonitorIDs {
			if visiveis[id] {
				monitores = append(monitores, id)
			}
		}
		for _, id := range jw.GroupIDs {
			if v.access.Group(id) {
				grupos = append(grupos, id)
			}
		}
		if len(monitores) == 0 && len(grupos) == 0 {
			continue
		}
		jw.MonitorIDs, jw.GroupIDs = monitores, grupos
		out = append(out, jw)
	}
	return out, nil
}

func (a *API) handleListMaintenance(w http.ResponseWriter, r *http.Request) {
	janelas, err := a.store.Maintenance().List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	janelas, err = a.visibleMaintenance(r, janelas)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	agora := a.clock.Now().UTC()
	items := make([]maintenanceResponse, 0, len(janelas))
	for _, jw := range janelas {
		items = append(items, toMaintenanceResponse(jw, agora))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) handleGetMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	jw, err := a.store.Maintenance().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	visiveis, err := a.visibleMaintenance(r, []domain.MaintenanceWindow{jw})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(visiveis) == 0 {
		writeStoreError(w, errOutOfScope)
		return
	}
	writeJSON(w, http.StatusOK, toMaintenanceResponse(visiveis[0], a.clock.Now().UTC()))
}

func (a *API) handleCreateMaintenance(w http.ResponseWriter, r *http.Request) {
	jw, ok := a.maintenanceFromRequest(w, r)
	if !ok {
		return
	}
	if err := a.store.Maintenance().Create(r.Context(), &jw); err != nil {
		writeStoreError(w, err)
		return
	}
	a.reloadMaintenance(r.Context())
	writeJSON(w, http.StatusCreated, toMaintenanceResponse(jw, a.clock.Now().UTC()))
}

func (a *API) handleUpdateMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	jw, ok := a.maintenanceFromRequest(w, r)
	if !ok {
		return
	}
	jw.ID = id
	if err := a.store.Maintenance().Update(r.Context(), jw); err != nil {
		writeStoreError(w, err)
		return
	}
	a.reloadMaintenance(r.Context())

	gravada, err := a.store.Maintenance().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMaintenanceResponse(gravada, a.clock.Now().UTC()))
}

// handleEndMaintenance encerra a janela agora, inclusive as próximas
// ocorrências da recorrente. Na que ainda não começou, é cancelar.
//
// Encerrar não apaga: o histórico continua dizendo que houve manutenção
// até aquela hora.
func (a *API) handleEndMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	agora := a.clock.Now().UTC().Truncate(time.Millisecond)
	if err := a.store.Maintenance().End(r.Context(), id, agora); err != nil {
		writeStoreError(w, err)
		return
	}
	a.reloadMaintenance(r.Context())

	jw, err := a.store.Maintenance().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toMaintenanceResponse(jw, agora))
}

func (a *API) handleDeleteMaintenance(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.store.Maintenance().Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	a.reloadMaintenance(r.Context())
	w.WriteHeader(http.StatusNoContent)
}
