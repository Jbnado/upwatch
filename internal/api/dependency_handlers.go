package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// ---------- monitores dependentes ----------

// storeLookup consulta monitores no banco para as regras de dependência.
//
// Sem recorte de escopo: o ciclo existe ou não independentemente de quem
// pergunta, e a resposta é só sim ou não. Erro de leitura conta como
// monitor inexistente, que para a subida — o pior caso é deixar passar um
// ciclo que o motor, que também para em ciclo, aguenta.
func (a *API) storeLookup(ctx context.Context) domain.MonitorLookup {
	return func(id int64) (domain.Monitor, bool) {
		m, err := a.store.Monitors().Get(ctx, id)
		return m, err == nil
	}
}

// checkParentCycle recusa o pai que depende, direta ou indiretamente, do
// próprio monitor.
func (a *API) checkParentCycle(ctx context.Context, m domain.Monitor) error {
	if domain.ParentCycle(m.ID, m.ParentID, a.storeLookup(ctx)) {
		return &domain.ValidationError{Field: "parent_id",
			Msg: "o pai escolhido depende deste monitor; isso fecharia um ciclo"}
	}
	return nil
}

// childrenRequest é o corpo de "definir pai" para vários monitores.
type childrenRequest struct {
	MonitorIDs []int64 `json:"monitor_ids"`
}

// handleSetChildren põe vários monitores sob o monitor do caminho.
//
// Existe para o caso comum de ligar as verificações de uma sonda ao sinal
// de vida dela de uma vez. Pela edição completa, a interface teria de
// reenviar a configuração inteira de cada monitor só para mudar o pai.
//
// Tudo ou nada: escopo e ciclo são conferidos para todos antes de gravar
// o primeiro, e o store grava numa transação. O filho de fora do escopo
// responde como o inexistente, pelo mesmo motivo do pai no cadastro.
func (a *API) handleSetChildren(w http.ResponseWriter, r *http.Request) {
	paiID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req childrenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.MonitorIDs) == 0 {
		writeFieldError(w, "monitor_ids", "informe ao menos um monitor")
		return
	}
	if len(req.MonitorIDs) > store.MaxPageSize {
		writeFieldError(w, "monitor_ids", fmt.Sprintf("no máximo %d monitores por pedido", store.MaxPageSize))
		return
	}

	ctx := r.Context()
	v := viewerFrom(r)
	vistos := map[int64]bool{}
	var ids []int64
	var anteriores []domain.Monitor
	for _, id := range req.MonitorIDs {
		if vistos[id] {
			continue
		}
		vistos[id] = true
		m, err := a.store.Monitors().Get(ctx, id)
		if err == nil && !v.sees(m) {
			err = errOutOfScope
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		ids = append(ids, id)
		anteriores = append(anteriores, m)
	}

	lookup := a.storeLookup(ctx)
	for _, m := range anteriores {
		if domain.ParentCycle(m.ID, &paiID, lookup) {
			writeFieldError(w, "monitor_ids",
				fmt.Sprintf("%s não pode depender deste monitor: isso fecharia um ciclo", m.Name))
			return
		}
	}

	if err := a.store.Monitors().SetParent(ctx, &paiID, ids); err != nil {
		writeStoreError(w, err)
		return
	}

	to := audienceOf(r)
	items := make([]monitorResponse, 0, len(ids))
	for i, id := range ids {
		m, err := a.store.Monitors().Get(ctx, id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		// O motor precisa do pai novo para decidir a próxima falha.
		a.notifyScheduler(m)
		anterior := anteriores[i]
		a.events.publish(event{Type: "monitor.updated", Data: toMonitorResponse(m, checker.Readers), monitor: &m, previous: &anterior})
		items = append(items, toMonitorResponse(m, to))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// affectedRef aponta o ancestral em queda que cala um monitor.
//
// O id é nulo, e o nome some, quando a conta não enxerga o ancestral: o
// filho de dentro do escopo mostra que está calado sem revelar o que
// existe fora dele.
type affectedRef struct {
	ID   *int64 `json:"id"`
	Name string `json:"name,omitempty"`
}

func affectedFor(v viewer, pai domain.Monitor) *affectedRef {
	if !v.sees(pai) {
		return &affectedRef{}
	}
	id := pai.ID
	return &affectedRef{ID: &id, Name: pai.Name}
}

// silenced devolve, para cada monitor ativo calado por um ancestral em
// queda, esse ancestral.
//
// Lê todos os monitores, sem recorte de escopo: o pai de um monitor
// visível pode estar fora do escopo, e o filho continua calado por ele.
// É a mesma regra do motor (domain.DownAncestor), aplicada ao estado
// gravado, para a tela mostrar exatamente o que o motor decidiu.
func (a *API) silenced(ctx context.Context, estados map[int64]domain.MonitorState) (map[int64]domain.Monitor, error) {
	todos, err := a.listAllMonitors(ctx, store.MonitorFilter{})
	if err != nil {
		return nil, err
	}
	porID := make(map[int64]domain.Monitor, len(todos))
	for _, m := range todos {
		porID[m.ID] = m
	}
	lookup := func(id int64) (domain.Monitor, bool) {
		m, ok := porID[id]
		return m, ok
	}
	state := func(id int64) domain.MonitorState { return estados[id] }

	calados := map[int64]domain.Monitor{}
	for _, m := range todos {
		if !m.Enabled || m.ParentID == nil {
			continue
		}
		if pai, ok := domain.DownAncestor(m, lookup, state); ok {
			calados[m.ID] = pai
		}
	}
	return calados, nil
}
