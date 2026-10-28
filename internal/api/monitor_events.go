package api

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

	// pos é a posição do evento na linha do tempo, de onde sai o cursor.
	pos eventCursor
}

// eventCursor é a posição de um evento na ordem da linha do tempo: mais
// recente primeiro; no mesmo instante, a mudança antes da falha que a
// confirmou ("caiu, porque respondeu X"); entre mudanças, id decrescente;
// entre falhas, origem decrescente.
//
// Composto, e não só o instante: mudança e falhas de várias origens caem
// no mesmo milissegundo, e "antes de" um instante perdia as que sobravam
// nele quando a página acabava no meio.
type eventCursor struct {
	at time.Time
	// rank 0 é mudança, 1 é falha.
	rank int
	// id é a chave da mudança; probe, a da falha.
	id    int64
	probe string
}

func (c eventCursor) compare(o eventCursor) int {
	if x := o.at.Compare(c.at); x != 0 {
		return x
	}
	if c.rank != o.rank {
		return c.rank - o.rank
	}
	if c.rank == 0 {
		return cmp.Compare(o.id, c.id)
	}
	return strings.Compare(o.probe, c.probe)
}

// String é o cursor como sai na resposta: opaco para quem consome, que só
// o devolve em ?cursor=.
func (c eventCursor) String() string {
	chave := c.probe
	if c.rank == 0 {
		chave = strconv.FormatInt(c.id, 10)
	}
	return fmt.Sprintf("%d:%d:%s", c.at.UnixMilli(), c.rank, chave)
}

func parseEventCursor(raw string) (eventCursor, error) {
	partes := strings.SplitN(raw, ":", 3)
	if len(partes) != 3 {
		return eventCursor{}, fmt.Errorf("cursor inválido")
	}
	ms, err := strconv.ParseInt(partes[0], 10, 64)
	if err != nil {
		return eventCursor{}, fmt.Errorf("cursor inválido")
	}
	c := eventCursor{at: time.UnixMilli(ms).UTC()}
	switch partes[1] {
	case "0":
		if c.id, err = strconv.ParseInt(partes[2], 10, 64); err != nil {
			return eventCursor{}, fmt.Errorf("cursor inválido")
		}
	case "1":
		c.rank, c.probe = 1, partes[2]
	default:
		return eventCursor{}, fmt.Errorf("cursor inválido")
	}
	return c, nil
}

// handleMonitorEvents devolve uma página da linha do tempo.
//
// Parâmetros: cursor (o next_cursor da página anterior) pagina para trás;
// before (RFC3339, exclusivo) começa de um instante; limit (1–200); type
// filtra por "state" ou "failure".
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

	var (
		transicoes = store.TransitionFilter{MonitorID: id, Limit: limit + 1}
		falhas     = store.HeartbeatQuery{
			MonitorID: id, Range: store.TimeRange{From: time.Unix(0, 0), To: farFuture},
			Limit: limit + 1, OnlyFailures: true,
		}
	)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := parseEventCursor(raw)
		if err != nil {
			writeFieldError(w, "cursor", "cursor inválido; use o next_cursor da página anterior")
			return
		}
		transicoes.Before = c.at
		if c.rank == 0 {
			// Parou numa mudança: faltam as mudanças anteriores a ela e
			// todas as falhas do mesmo instante, que vêm depois na ordem.
			transicoes.BeforeID = c.id
			falhas.Range.To = c.at.Add(time.Millisecond)
		} else {
			// Parou numa falha: as mudanças do instante já saíram.
			falhas.Before = &store.HeartbeatCursor{At: c.at, ProbeID: c.probe}
		}
	} else if raw := r.URL.Query().Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeFieldError(w, "before", "data inválida; use RFC3339")
			return
		}
		transicoes.Before, falhas.Range.To = t.UTC(), t.UTC()
	}

	var items []monitorEvent

	if tipo != "failure" {
		mudancas, err := a.store.Transitions().List(r.Context(), transicoes)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for _, t := range mudancas {
			items = append(items, monitorEvent{
				Type: "state", At: t.At, Status: t.To.String(), From: t.From.String(), Message: t.Cause,
				pos: eventCursor{at: t.At, id: t.ID},
			})
		}
	}

	if tipo != "state" {
		batidas, err := a.store.QueryHeartbeats(r.Context(), falhas)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for _, hb := range batidas {
			latencia := hb.LatencyMS
			items = append(items, monitorEvent{
				Type: "failure", At: hb.Timestamp, Status: hb.Status.String(),
				Message: hb.Message, LatencyMS: &latencia, ProbeID: hb.ProbeID,
				pos: eventCursor{at: hb.Timestamp, rank: 1, probe: hb.ProbeID},
			})
		}
	}

	slices.SortFunc(items, func(x, y monitorEvent) int { return x.pos.compare(y.pos) })

	// Cada fonte trouxe até limit+1: se o total passou do limite, há
	// próxima página, e ela começa logo depois do último evento desta.
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if items == nil {
		items = []monitorEvent{}
	}
	body := map[string]any{"items": items, "has_more": hasMore}
	if hasMore {
		body["next_cursor"] = items[len(items)-1].pos.String()
	}
	writeJSON(w, http.StatusOK, body)
}
