package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/Jbnado/upwatch/internal/store"
)

// Linha do tempo do monitor: mudanças confirmadas de estado e batidas com
// falha, cada uma com a causa, da mais recente para trás.
//
// As duas fontes contam coisas diferentes. A mudança diz "desde quando
// está fora"; a batida com falha diz "o que respondeu agora há pouco".
// Juntas respondem à pergunta de quem abre o monitor durante a queda, sem
// depender da janela do gráfico — que era o que fazia a tela dizer
// "nenhuma mudança no período" com o monitor fora do ar.

const (
	defaultEventsLimit = 50
	maxEventsLimit     = 200
)

// farFuture é o fim aberto da janela quando o cliente não pagina. Em vez
// do relógio: uma batida com horário um pouco adiantado não pode sumir da
// primeira página.
var farFuture = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

type monitorEvent struct {
	// Type é "state" (mudança confirmada) ou "failure" (batida com falha).
	Type   string    `json:"type"`
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	// From é o estado anterior, só nas mudanças.
	From    string `json:"from,omitempty"`
	Message string `json:"message"`
	// LatencyMS e ProbeID só nas falhas.
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	ProbeID   string `json:"probe_id,omitempty"`
}

// handleMonitorEvents devolve uma página da linha do tempo.
//
// Parâmetros: before (RFC3339, exclusivo) pagina para trás; limit (1–200);
// type filtra por "state" ou "failure".
func (a *API) handleMonitorEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	tipo := r.URL.Query().Get("type")
	if tipo != "" && tipo != "state" && tipo != "failure" {
		writeFieldError(w, "type", "tipo inválido; use state ou failure")
		return
	}

	limit := int(queryInt64(r, "limit", defaultEventsLimit))
	if limit <= 0 {
		limit = defaultEventsLimit
	}
	limit = min(limit, maxEventsLimit)

	before := farFuture
	if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeFieldError(w, "before", "data inválida; use RFC3339")
			return
		}
		before = t.UTC()
	}

	var items []monitorEvent

	if tipo != "failure" {
		filtro := store.TransitionFilter{MonitorID: id, Limit: limit + 1}
		if before != farFuture {
			filtro.Before = before
		}
		mudancas, err := a.store.Transitions().List(r.Context(), filtro)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for _, t := range mudancas {
			items = append(items, monitorEvent{
				Type: "state", At: t.At, Status: t.To.String(), From: t.From.String(), Message: t.Cause,
			})
		}
	}

	if tipo != "state" {
		falhas, err := a.store.QueryHeartbeats(r.Context(), store.HeartbeatQuery{
			MonitorID:    id,
			Range:        store.TimeRange{From: time.Unix(0, 0), To: before},
			Limit:        limit + 1,
			OnlyFailures: true,
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for _, hb := range falhas {
			latencia := hb.LatencyMS
			items = append(items, monitorEvent{
				Type: "failure", At: hb.Timestamp, Status: hb.Status.String(),
				Message: hb.Message, LatencyMS: &latencia, ProbeID: hb.ProbeID,
			})
		}
	}

	// Mais recente primeiro; no mesmo instante, a mudança antes da falha
	// que a confirmou — é a ordem em que se lê "caiu, porque respondeu X".
	slices.SortStableFunc(items, func(x, y monitorEvent) int {
		if c := y.At.Compare(x.At); c != 0 {
			return c
		}
		return eventRank(x) - eventRank(y)
	})

	page, hasMore := cutEvents(items, limit)
	body := map[string]any{"items": page, "has_more": hasMore}
	if hasMore {
		body["next_before"] = page[len(page)-1].At
	}
	writeJSON(w, http.StatusOK, body)
}

func eventRank(e monitorEvent) int {
	if e.Type == "state" {
		return 0
	}
	return 1
}

// cutEvents corta a página sem separar eventos do mesmo instante.
//
// A página seguinte pede "antes de" o último instante desta; cortar no
// meio de um instante faria os eventos restantes dele nunca aparecerem.
func cutEvents(items []monitorEvent, limit int) ([]monitorEvent, bool) {
	if items == nil {
		return []monitorEvent{}, false
	}
	if len(items) <= limit {
		return items, false
	}
	fim := limit
	for fim < len(items) && items[fim].At.Equal(items[limit-1].At) {
		fim++
	}
	return items[:fim], fim < len(items)
}
