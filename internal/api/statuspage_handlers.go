package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// Administração das páginas públicas.
//
// Tudo aqui exige sessão. O que estes endpoints escrevem é exatamente o
// que a rota pública depois exibe, então errar aqui é o caminho indireto
// para vazar — daí a validação acontecer no domínio, num lugar só, e
// estes handlers apenas a chamarem.

// ---------- páginas ----------

type statusPageRequest struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ShowLatency *bool  `json:"show_latency,omitempty"`
	TimeZone    string `json:"time_zone"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

func (req statusPageRequest) toDomain() domain.StatusPage {
	// Nasce ligada e sem latência: o padrão seguro é publicar o estado e
	// não os números de desempenho.
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	showLatency := false
	if req.ShowLatency != nil {
		showLatency = *req.ShowLatency
	}

	return domain.StatusPage{
		Slug: req.Slug, Title: req.Title, Description: req.Description,
		ShowLatency: showLatency, TimeZone: req.TimeZone, Enabled: enabled,
	}
}

func (a *API) handleListStatusPages(w http.ResponseWriter, r *http.Request) {
	paginas, err := a.store.StatusPages().List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if paginas == nil {
		paginas = []domain.StatusPage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": paginas})
}

func (a *API) handleCreateStatusPage(w http.ResponseWriter, r *http.Request) {
	var req statusPageRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	p := req.toDomain()
	if err := p.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.StatusPages().Create(r.Context(), &p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// handleGetStatusPage devolve a página com grupos e componentes.
//
// Tudo numa resposta só porque a tela de administração precisa dos três
// para desenhar qualquer coisa; três requisições seriam três estados de
// carregamento para montar uma lista.
func (a *API) handleGetStatusPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	p, err := a.store.StatusPages().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	grupos, err := a.store.StatusPages().Groups(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	comps, err := a.store.StatusPages().Components(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if grupos == nil {
		grupos = []domain.StatusPageGroup{}
	}
	if comps == nil {
		comps = []domain.StatusPageComponent{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"page": p, "groups": grupos, "components": comps,
	})
}

func (a *API) handleUpdateStatusPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req statusPageRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	p := req.toDomain()
	p.ID = id
	if err := p.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.StatusPages().Update(r.Context(), p); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *API) handleDeleteStatusPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.store.StatusPages().Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- grupos ----------

type groupRequest struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
}

func (a *API) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	pageID, ok := pathID(w, r)
	if !ok {
		return
	}

	var req groupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	g := domain.StatusPageGroup{PageID: pageID, Name: req.Name, Position: req.Position}
	if err := g.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.StatusPages().CreateGroup(r.Context(), &g); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (a *API) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	pageID, ok := pathID(w, r)
	if !ok {
		return
	}
	groupID, ok := paramID(w, r, "groupID")
	if !ok {
		return
	}

	var req groupRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	g := domain.StatusPageGroup{ID: groupID, PageID: pageID, Name: req.Name, Position: req.Position}
	if err := g.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.StatusPages().UpdateGroup(r.Context(), g); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (a *API) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := pathID(w, r); !ok {
		return
	}
	groupID, ok := paramID(w, r, "groupID")
	if !ok {
		return
	}

	if err := a.store.StatusPages().DeleteGroup(r.Context(), groupID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- componentes ----------

type componentRequest struct {
	GroupID  *int64 `json:"group_id,omitempty"`
	Label    string `json:"label"`
	Position int    `json:"position"`
}

// handleSetComponent publica um monitor na página.
//
// PUT e não POST: publicar duas vezes o mesmo alvo precisa atualizar o
// vínculo, não criar um segundo.
func (a *API) handleSetComponent(w http.ResponseWriter, r *http.Request) {
	pageID, ok := pathID(w, r)
	if !ok {
		return
	}
	monitorID, ok := paramID(w, r, "monitorID")
	if !ok {
		return
	}

	var req componentRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	c := domain.StatusPageComponent{
		PageID: pageID, MonitorID: monitorID,
		GroupID: req.GroupID, Label: req.Label, Position: req.Position,
	}
	if err := c.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	// Confere que o monitor existe antes de publicar: sem isto, um id
	// errado criaria um componente que a página pública não conseguiria
	// montar.
	if _, err := a.store.Monitors().Get(r.Context(), monitorID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.StatusPages().SetComponent(r.Context(), c); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) handleRemoveComponent(w http.ResponseWriter, r *http.Request) {
	pageID, ok := pathID(w, r)
	if !ok {
		return
	}
	monitorID, ok := paramID(w, r, "monitorID")
	if !ok {
		return
	}

	if err := a.store.StatusPages().RemoveComponent(r.Context(), pageID, monitorID); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- relatos ----------

type announcementRequest struct {
	Title      string  `json:"title"`
	Impact     string  `json:"impact"`
	Phase      string  `json:"phase"`
	Global     bool    `json:"global"`
	Components []int64 `json:"components"`
	IncidentID *int64  `json:"incident_id,omitempty"`
	// Body, quando presente, publica a primeira entrada da linha do tempo
	// junto com o relato: abrir um incidente e não dizer nada é o pior
	// momento para exigir duas requisições.
	Body string `json:"body,omitempty"`
}

func (a *API) handleListAnnouncements(w http.ResponseWriter, r *http.Request) {
	f := store.AnnouncementFilter{
		Page: store.PageFilter{
			AfterID: queryInt64(r, "after_id", 0),
			Limit:   int(queryInt64(r, "limit", 0)),
		},
		OnlyOpen: r.URL.Query().Get("open") == "true",
	}

	page, err := a.store.Announcements().List(r.Context(), f)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if page.Items == nil {
		page.Items = []domain.Announcement{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": page.Items, "has_more": page.HasMore})
}

func (a *API) handleCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req announcementRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	an, ok := a.announcementFromRequest(w, req)
	if !ok {
		return
	}
	an.StartedAt = a.clock.Now().UTC()
	if an.Phase == domain.PhaseResolved {
		at := an.StartedAt
		an.ResolvedAt = &at
	}

	if err := a.store.Announcements().Create(r.Context(), &an); err != nil {
		writeStoreError(w, err)
		return
	}

	if req.Body != "" {
		u := domain.AnnouncementUpdate{
			AnnouncementID: an.ID, Phase: an.Phase,
			Body: req.Body, PublishedAt: an.StartedAt,
		}
		if err := u.Validate(); err != nil {
			writeStoreError(w, err)
			return
		}
		if err := a.store.Announcements().AddUpdate(r.Context(), &u); err != nil {
			writeStoreError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusCreated, an)
}

func (a *API) handleGetAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	an, err := a.store.Announcements().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	updates, err := a.store.Announcements().Updates(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if updates == nil {
		updates = []domain.AnnouncementUpdate{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"announcement": an, "updates": updates})
}

func (a *API) handleUpdateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req announcementRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	atual, err := a.store.Announcements().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	an, ok := a.announcementFromRequest(w, req)
	if !ok {
		return
	}
	an.ID = id
	// StartedAt não se reescreve: é quando a queda começou, e mexer nele
	// reescreveria o histórico que a página publicou.
	an.StartedAt = atual.StartedAt
	an.ResolvedAt = resolvedStamp(atual, an.Phase, a.clock.Now().UTC())

	if err := a.store.Announcements().Update(r.Context(), an); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, an)
}

func (a *API) handleDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := a.store.Announcements().Delete(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type announcementUpdateRequest struct {
	Phase string `json:"phase"`
	Body  string `json:"body"`
}

// handlePublishUpdate acrescenta uma entrada à linha do tempo.
//
// A fase do relato acompanha a da entrada publicada. Manter as duas
// separadas obrigaria a lembrar de mudar o estado depois de escrever o
// texto, e o esquecimento apareceria numa página pública dizendo
// "investigando" sob uma atualização que diz "resolvido".
func (a *API) handlePublishUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req announcementUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	fase, err := domain.ParseIncidentPhase(req.Phase)
	if err != nil {
		writeFieldError(w, "phase", "fase desconhecida")
		return
	}

	an, err := a.store.Announcements().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	agora := a.clock.Now().UTC()
	u := domain.AnnouncementUpdate{
		AnnouncementID: id, Phase: fase, Body: req.Body, PublishedAt: agora,
	}
	if err := u.Validate(); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.store.Announcements().AddUpdate(r.Context(), &u); err != nil {
		writeStoreError(w, err)
		return
	}

	an.ResolvedAt = resolvedStamp(an, fase, agora)
	an.Phase = fase
	if err := a.store.Announcements().Update(r.Context(), an); err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{"announcement": an, "update": u})
}

// announcementFromRequest valida e converte o corpo.
func (a *API) announcementFromRequest(w http.ResponseWriter, req announcementRequest) (domain.Announcement, bool) {
	impacto, err := domain.ParseIncidentImpact(req.Impact)
	if err != nil {
		writeFieldError(w, "impact", "impacto desconhecido")
		return domain.Announcement{}, false
	}
	fase, err := domain.ParseIncidentPhase(req.Phase)
	if err != nil {
		writeFieldError(w, "phase", "fase desconhecida")
		return domain.Announcement{}, false
	}

	an := domain.Announcement{
		Title: req.Title, Impact: impacto, Phase: fase,
		Global: req.Global, Components: req.Components, IncidentID: req.IncidentID,
	}
	if err := an.Validate(); err != nil {
		writeStoreError(w, err)
		return domain.Announcement{}, false
	}
	return an, true
}

// resolvedStamp decide o carimbo de encerramento a partir da fase.
//
// Derivado, e não recebido do cliente: dois lugares guardando o mesmo
// fato divergem, e a página passaria a mostrar "resolvido" sem hora, ou
// hora sem "resolvido".
func resolvedStamp(atual domain.Announcement, fase domain.IncidentPhase, agora time.Time) *time.Time {
	if fase != domain.PhaseResolved {
		// Reabrir limpa o carimbo: um relato que voltou a investigar não
		// tem hora de encerramento.
		return nil
	}
	if atual.ResolvedAt != nil {
		// Já estava resolvido: manter o carimbo original, senão editar o
		// texto moveria a hora do encerramento.
		return atual.ResolvedAt
	}
	return &agora
}

// paramID lê um parâmetro numérico da rota.
func paramID(w http.ResponseWriter, r *http.Request, nome string) (int64, bool) {
	valor := chi.URLParam(r, nome)

	id, err := strconv.ParseInt(valor, 10, 64)
	if err != nil || id <= 0 {
		writeFieldError(w, nome, "identificador inválido")
		return 0, false
	}
	return id, true
}
