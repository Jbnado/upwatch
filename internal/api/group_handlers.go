package api

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// Grupos de monitores e o que a tela inicial lê deles.
//
// Os nomes dos handlers levam "MonitorGroup" porque "Group" já nomeia os
// grupos de componentes das páginas públicas, que são outra coisa: um
// agrupamento editorial de uma página, não a organização da instalação.

type monitorGroupRequest struct {
	Name string `json:"name"`
	// Slug vazio é derivado do nome.
	Slug     string `json:"slug,omitempty"`
	ParentID *int64 `json:"parent_id,omitempty"`
}

func (req monitorGroupRequest) toDomain() domain.Group {
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		slug = domain.Slugify(req.Name)
	}
	return domain.Group{Name: strings.TrimSpace(req.Name), Slug: slug, ParentID: req.ParentID}
}

type groupResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ParentID *int64 `json:"parent_id"`
	// Path é o nome completo ("RPA / prod"). Vem pronto para nenhum
	// cliente precisar remontar a árvore para escrever um título.
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toGroupResponse(g domain.Group, tree domain.GroupTree) groupResponse {
	path := tree.Path(g.ID)
	if path == "" {
		path = g.Name
	}
	return groupResponse{
		ID: g.ID, Name: g.Name, Slug: g.Slug, ParentID: g.ParentID, Path: path,
		CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

// handleListGroups devolve os grupos do escopo, em ordem de caminho.
func (a *API) handleListGroups(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r)
	items := []groupResponse{}
	for _, g := range v.groups.All() {
		if v.access.Group(g.ID) {
			items = append(items, toGroupResponse(g, v.groups))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) handleGetMonitorGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v := viewerFrom(r)
	g, _ := v.groups.Get(id)
	writeJSON(w, http.StatusOK, toGroupResponse(g, v.groups))
}

func (a *API) handleCreateMonitorGroup(w http.ResponseWriter, r *http.Request) {
	var req monitorGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	g := req.toDomain()
	if !a.placeGroup(w, r, g, nil) {
		return
	}

	if err := a.store.Groups().Create(r.Context(), &g); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toGroupResponse(g, a.treeWith(r, g)))
}

func (a *API) handleUpdateMonitorGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req monitorGroupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	anterior, _ := viewerFrom(r).groups.Get(id)
	g := req.toDomain()
	g.ID = id
	g.CreatedAt = anterior.CreatedAt
	if !a.placeGroup(w, r, g, &anterior) {
		return
	}

	if err := a.store.Groups().Update(r.Context(), g); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGroupResponse(g, a.treeWith(r, g)))
}

// placeGroup valida o grupo e o lugar pedido para ele.
func (a *API) placeGroup(w http.ResponseWriter, r *http.Request, g domain.Group, anterior *domain.Group) bool {
	if err := g.Validate(); err != nil {
		writeStoreError(w, err)
		return false
	}
	if err := viewerFrom(r).checkGroupPlacement(g, anterior); err != nil {
		writeScopeError(w, err)
		return false
	}
	if g.ParentID == nil || (anterior != nil && anterior.ParentID != nil && *anterior.ParentID == *g.ParentID) {
		return true
	}

	// O pai vai deixar de ser folha. Se tiver monitores, eles ficariam
	// num grupo que não é mais ambiente; quem reorganiza move antes.
	page, err := a.store.Monitors().List(r.Context(), store.MonitorFilter{
		Page:   store.PageFilter{Limit: 1},
		Groups: &store.GroupSelection{IDs: []int64{*g.ParentID}},
	})
	if err != nil {
		writeStoreError(w, err)
		return false
	}
	if len(page.Items) > 0 {
		writeError(w, http.StatusConflict, codeConflict,
			"o grupo pai já tem monitores; mova-os para um ambiente antes de criar ambientes nele")
		return false
	}
	return true
}

// treeWith devolve a árvore da requisição com o grupo recém-gravado, para
// o caminho da resposta já refletir a mudança.
func (a *API) treeWith(r *http.Request, g domain.Group) domain.GroupTree {
	todos := viewerFrom(r).groups.All()
	out := make([]domain.Group, 0, len(todos)+1)
	for _, x := range todos {
		if x.ID != g.ID {
			out = append(out, x)
		}
	}
	return domain.NewGroupTree(append(out, g))
}

func (a *API) handleDeleteMonitorGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	err := a.store.Groups().Delete(r.Context(), id)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, codeConflict,
			"o grupo ainda tem monitores ou ambientes; mova-os antes de apagar")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- canais do grupo ----------

func (a *API) handleListGroupChannels(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	canais, err := a.store.Channels().ForGroup(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if canais == nil {
		canais = []domain.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": canais})
}

func (a *API) handleLinkGroupChannel(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathID(w, r)
	if !ok {
		return
	}
	channelID, ok := pathParamID(w, r, "channelID")
	if !ok {
		return
	}
	if !a.channelExists(w, r, channelID) {
		return
	}
	if err := a.store.Channels().LinkGroup(r.Context(), groupID, channelID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleUnlinkGroupChannel(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathID(w, r)
	if !ok {
		return
	}
	channelID, ok := pathParamID(w, r, "channelID")
	if !ok {
		return
	}
	if !a.channelExists(w, r, channelID) {
		return
	}
	if err := a.store.Channels().UnlinkGroup(r.Context(), groupID, channelID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- resumo por grupo ----------

// groupSummary é o cartão de um grupo na tela inicial.
type groupSummary struct {
	// GroupID nulo é "Sem grupo".
	GroupID  *int64 `json:"group_id"`
	ParentID *int64 `json:"parent_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	// Status é o pior estado entre os monitores ativos, na ordem fora do
	// ar, degradado, sem medição, no ar.
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Up       int    `json:"up"`
	Degraded int    `json:"degraded"`
	Down     int    `json:"down"`
	Unknown  int    `json:"unknown"`
	// Paused não conta como problema: pausar é decisão de alguém.
	Paused int `json:"paused"`
	// Problems é fora do ar + degradado + sem medição, a palavra do cartão
	// ("2 problemas").
	Problems int `json:"problems"`

	statuses []domain.Status
}

func (g *groupSummary) add(m domain.Monitor, s domain.Status) {
	g.Total++
	if !m.Enabled {
		g.Paused++
		return
	}
	g.statuses = append(g.statuses, s)
	switch s {
	case domain.StatusUp:
		g.Up++
	case domain.StatusDegraded:
		g.Degraded++
	case domain.StatusDown:
		g.Down++
	default:
		g.Unknown++
	}
}

func (g *groupSummary) close() {
	g.Problems = g.Down + g.Degraded + g.Unknown
	if len(g.statuses) == 0 && g.Total > 0 {
		// Só pausados: nada está sendo medido, e dizer "no ar" seria
		// afirmar o que ninguém conferiu.
		g.Status = domain.StatusUnknown.String()
		return
	}
	g.Status = domain.WorstStatus(g.statuses).String()
}

// handleGroupSummary devolve um cartão por grupo do escopo.
//
// O sistema soma os ambientes dele; "Sem grupo" aparece só para quem
// alcança tudo, e só quando há monitor nele.
func (a *API) handleGroupSummary(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r)

	monitores, err := a.listAllMonitors(r.Context(), store.MonitorFilter{Groups: v.monitorSelection()})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	estados, err := a.store.States().All(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}

	cartoes := map[int64]*groupSummary{}
	var ordem []*groupSummary
	for _, g := range v.groups.All() {
		if !v.access.Group(g.ID) {
			continue
		}
		c := &groupSummary{GroupID: &g.ID, ParentID: g.ParentID, Name: g.Name, Path: v.groups.Path(g.ID)}
		cartoes[g.ID] = c
		ordem = append(ordem, c)
	}
	semGrupo := &groupSummary{Name: "Sem grupo", Path: "Sem grupo"}

	for _, m := range monitores {
		s := estados[m.ID].Status
		if m.GroupID == nil {
			semGrupo.add(m, s)
			continue
		}
		if c, ok := cartoes[*m.GroupID]; ok {
			c.add(m, s)
		}
		if g, ok := v.groups.Get(*m.GroupID); ok && g.ParentID != nil {
			if pai, ok := cartoes[*g.ParentID]; ok {
				pai.add(m, s)
			}
		}
	}

	items := make([]groupSummary, 0, len(ordem)+1)
	for _, c := range ordem {
		c.close()
		items = append(items, *c)
	}
	if semGrupo.Total > 0 && v.access.All() {
		semGrupo.close()
		items = append(items, *semGrupo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---------- problemas agora ----------

// problem é uma linha de "Problemas agora".
type problem struct {
	MonitorID int64  `json:"monitor_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	GroupID   *int64 `json:"group_id"`
	// GroupPath vazio é "Sem grupo".
	GroupPath string `json:"group_path"`
	Status    string `json:"status"`
	// Since é desde quando o estado vale; para o monitor nunca medido, o
	// cadastro.
	Since time.Time `json:"since"`
	// Cause é a mensagem da última verificação com falha, ou a causa da
	// queda em curso.
	Cause       string     `json:"cause"`
	LastCheckAt *time.Time `json:"last_check_at"`

	rank int
}

// handleProblems lista o que está errado agora: fora do ar, degradado e
// sem medição, nessa ordem, e dentro de cada estado o mais antigo
// primeiro. Monitor pausado não entra.
func (a *API) handleProblems(w http.ResponseWriter, r *http.Request) {
	v := viewerFrom(r)
	ctx := r.Context()

	ativo := true
	monitores, err := a.listAllMonitors(ctx, store.MonitorFilter{Groups: v.monitorSelection(), Enabled: &ativo})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	estados, err := a.store.States().All(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ultimas, err := a.store.LatestHeartbeats(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	abertos, err := a.store.Incidents().List(ctx, store.IncidentFilter{
		OnlyOpen: true, Groups: v.monitorSelection(),
		Page: store.PageFilter{Limit: store.MaxPageSize},
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	causaDaQueda := map[int64]string{}
	for _, i := range abertos.Items {
		causaDaQueda[i.MonitorID] = i.Cause
	}

	items := []problem{}
	for _, m := range monitores {
		estado := estados[m.ID]
		if estado.Status == domain.StatusUp {
			continue
		}
		p := problem{
			MonitorID: m.ID, Name: m.Name, Type: m.Type.String(), GroupID: m.GroupID,
			Status: estado.Status.String(), Since: estado.Since, rank: domain.ProblemRank(estado.Status),
		}
		if m.GroupID != nil {
			p.GroupPath = v.groups.Path(*m.GroupID)
		}
		if p.Since.IsZero() {
			p.Since = m.CreatedAt
		}
		if hb, ok := ultimas[m.ID]; ok {
			quando := hb.Timestamp
			p.LastCheckAt = &quando
			if hb.Status != domain.StatusUp {
				p.Cause = hb.Message
			}
		}
		if p.Cause == "" {
			p.Cause = causaDaQueda[m.ID]
		}
		items = append(items, p)
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank > items[j].rank
		}
		if !items[i].Since.Equal(items[j].Since) {
			return items[i].Since.Before(items[j].Since)
		}
		return items[i].MonitorID < items[j].MonitorID
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
