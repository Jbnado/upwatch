package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// ackRequest é o corpo do reconhecimento. O corpo inteiro é opcional:
// quem reconhece pelo celular, às três da manhã, não deveria precisar
// escrever nada.
type ackRequest struct {
	Note string `json:"note,omitempty"`
}

// handleAcknowledgeIncident registra o "estou vendo" sobre a queda aberta.
//
// O incidente vem pelo id, e não "a queda atual do monitor": quem clicou
// estava olhando aquela queda, e se ela acabou e outra começou no meio, o
// reconhecimento não pode passar para uma queda que a pessoa nem viu.
//
// Pendurado no monitor para a guarda de escopo valer sem política nova, e
// na rota de escrita porque reconhecer cala o lembrete dos outros:
// observador não reconhece.
func (a *API) handleAcknowledgeIncident(w http.ResponseWriter, r *http.Request) {
	m := monitorFrom(r)
	incidentID, ok := pathParamID(w, r, "incidentID")
	if !ok {
		return
	}

	var req ackRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}

	atual, err := a.store.Incidents().Get(r.Context(), incidentID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && atual.MonitorID != m.ID) {
		writeError(w, http.StatusNotFound, codeNotFound, "incidente não encontrado")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	ack := domain.Acknowledgement{
		By:   mustUser(r).Username,
		At:   a.clock.Now().UTC().Truncate(time.Millisecond),
		Note: strings.TrimSpace(req.Note),
	}
	if err := ack.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}

	inc, err := a.store.Incidents().Acknowledge(r.Context(), incidentID, ack)
	if errors.Is(err, store.ErrConflict) {
		writeAckConflict(w, inc)
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIncidentResponse(inc, a.clock.Now()))
}

// writeAckConflict diz por que o reconhecimento não entrou. "Já existe"
// genérico não ajuda quem chegou para olhar a queda: saber quem chegou
// antes é o que evita duas pessoas mexendo no mesmo problema.
func writeAckConflict(w http.ResponseWriter, inc domain.Incident) {
	if inc.Acknowledgement != nil {
		writeError(w, http.StatusConflict, codeConflict,
			"incidente já reconhecido por %s", inc.Acknowledgement.By)
		return
	}
	writeError(w, http.StatusConflict, codeConflict, "o incidente já foi encerrado")
}

// minutesToDuration lê o intervalo de lembrete da API. Nulo segue nulo:
// é "herda", e não zero, que é "desligado".
func minutesToDuration(min *int) *time.Duration {
	if min == nil {
		return nil
	}
	d := time.Duration(*min) * time.Minute
	return &d
}

func durationToMinutes(d *time.Duration) *int {
	if d == nil {
		return nil
	}
	min := int(d.Minutes())
	return &min
}
