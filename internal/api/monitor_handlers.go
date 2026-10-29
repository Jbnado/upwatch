package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
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
	// GroupID nulo é "Sem grupo". Como o resto do corpo, o PUT sobrescreve:
	// omitir move o monitor para "Sem grupo".
	GroupID *int64 `json:"group_id,omitempty"`
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
		GroupID:               req.GroupID,
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
	GroupID               *int64          `json:"group_id"`
	Enabled               bool            `json:"enabled"`
	Tags                  []string        `json:"tags"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`

	// NextExpectedPush é a próxima execução prevista de um push com
	// agenda. Só aparece no detalhe, na criação e na edição: é ali que um
	// fuso ou uma expressão errados precisam ficar visíveis, e não na
	// primeira madrugada em que o aviso deveria ter chegado.
	NextExpectedPush *time.Time `json:"next_expected_push,omitempty"`
}

// toMonitorResponse monta a representação pública do monitor.
//
// A configuração sai sem credencial: é por aqui que passam o GET, a
// listagem, a resposta da escrita e o fluxo de eventos, e omitir num lugar
// só garante que nenhum deles a esqueça.
//
// to diz para quem: o administrador recebe de volta o token do push,
// porque cadastra a rotina e copia o endereço; quem só lê, e o fluxo de
// eventos, que vai para todo mundo conectado, não. O que é segredo em
// cada tipo, e para quem, quem decide é checker.RedactConfig.
func toMonitorResponse(m domain.Monitor, to checker.Audience) monitorResponse {
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
		Config:                checker.RedactConfig(m.Type, m.Config, to),
		ParentID:              m.ParentID,
		GroupID:               m.GroupID,
		Enabled:               m.Enabled, Tags: tags,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// detailResponse é a resposta de um monitor isolado, com o que só faz
// sentido calcular para um de cada vez.
func (a *API) detailResponse(m domain.Monitor, to checker.Audience) monitorResponse {
	resp := toMonitorResponse(m, to)
	if m.Type != domain.MonitorPush {
		return resp
	}
	// Configuração que não interpreta aqui já foi recusada no cadastro;
	// se aparecer, a falta do campo é a resposta menos enganosa.
	if next, ok, err := checker.NextPushRun(m.Config, a.clock.Now()); err == nil && ok {
		resp.NextExpectedPush = &next
	}
	return resp
}

// handleListMonitors devolve uma página de monitores.
func (a *API) handleListMonitors(w http.ResponseWriter, r *http.Request) {
	filter := store.MonitorFilter{
		Page: store.PageFilter{
			AfterID: queryInt64(r, "after_id", 0),
			Limit:   int(queryInt64(r, "limit", 0)),
		},
		Tag:    r.URL.Query().Get("tag"),
		Groups: viewerFrom(r).monitorSelection(),
	}
	if raw := r.URL.Query().Get("enabled"); raw != "" {
		enabled := raw == "true"
		filter.Enabled = &enabled
	}
	if groupID := queryInt64(r, "group_id", 0); groupID > 0 {
		// O grupo pedido é recortado pelo escopo: pedir explicitamente o
		// grupo dos outros devolve lista vazia, como um grupo sem nada.
		filter.Groups = viewerFrom(r).selectGroup(groupID)
	}

	page, err := a.store.Monitors().List(r.Context(), filter)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	to := audienceOf(r)
	items := make([]monitorResponse, 0, len(page.Items))
	for _, m := range page.Items {
		items = append(items, toMonitorResponse(m, to))
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
	// Só a grafia canônica da configuração é gravada: é ela que a
	// ocultação e a preservação da senha conhecem (ver CarrySecrets).
	cfg, err := checker.CanonicalConfig(m)
	if err != nil {
		writeFieldError(w, "config", err.Error())
		return
	}
	m.Config = cfg
	if !a.validateMonitor(w, &m) || !a.pushTokenFree(w, r, m) {
		return
	}
	if err := viewerFrom(r).checkMonitorPlacement(m.GroupID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.checkMonitorParent(r.Context(), viewerFrom(r), m.ParentID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.checkTargetReach(r.Context(), viewerFrom(r), m, nil); err != nil {
		writeStoreError(w, err)
		return
	}
	m.RestrictInternal = a.restrictInternal(viewerFrom(r), m, nil)

	if err := a.store.Monitors().Create(r.Context(), &m); err != nil {
		writeStoreError(w, err)
		return
	}

	a.notifyScheduler(m)
	a.events.publish(event{Type: "monitor.created", Data: toMonitorResponse(m, checker.Readers), monitor: &m})
	writeJSON(w, http.StatusCreated, a.detailResponse(m, checker.Admins))
}

// handleGetMonitor devolve um monitor, já lido e conferido pela guarda
// de escopo.
func (a *API) handleGetMonitor(w http.ResponseWriter, r *http.Request) {
	if _, ok := pathID(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.detailResponse(monitorFrom(r), audienceOf(r)))
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
	if !a.carrySecrets(w, r, &m) {
		return
	}
	if !a.validateMonitor(w, &m) || !a.pushTokenFree(w, r, m) {
		return
	}
	if err := viewerFrom(r).checkMonitorPlacement(m.GroupID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := a.checkMonitorParent(r.Context(), viewerFrom(r), m.ParentID); err != nil {
		writeStoreError(w, err)
		return
	}
	anterior := monitorFrom(r)
	if err := a.checkTargetReach(r.Context(), viewerFrom(r), m, &anterior); err != nil {
		writeStoreError(w, err)
		return
	}
	m.RestrictInternal = a.restrictInternal(viewerFrom(r), m, &anterior)

	if err := a.store.Monitors().Update(r.Context(), m); err != nil {
		writeStoreError(w, err)
		return
	}

	// Relido do banco: o corpo da edição não traz a data de criação, e o
	// push conta dela a primeira janela. Entregar ao agendador o monitor
	// montado do pedido deixaria um push sem sinal "aguardando" para
	// sempre só porque alguém mudou o nome.
	m, err = a.store.Monitors().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	a.notifyScheduler(m)
	a.events.publish(event{Type: "monitor.updated", Data: toMonitorResponse(m, checker.Readers), monitor: &m, previous: &anterior})
	writeJSON(w, http.StatusOK, a.detailResponse(m, checker.Admins))
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
	anterior := monitorFrom(r)
	a.events.publish(event{Type: "monitor.deleted", Data: map[string]int64{"id": id}, previous: &anterior})
	w.WriteHeader(http.StatusNoContent)
}

// validateMonitor aplica as invariantes do domínio e as do checker.
//
// A validação específica do tipo acontece no cadastro, não na primeira
// execução: descobrir uma regex inválida só quando o monitor rodar
// deixaria um alvo sem vigilância sem que nada avisasse.
func (a *API) validateMonitor(w http.ResponseWriter, m *domain.Monitor) bool {
	// O token vai para a coluna que o /push consulta. Quem o lê da
	// configuração é o checker, o único que a interpreta.
	m.PushToken = checker.PushTokenOf(*m)

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

// carrySecrets mantém a senha guardada quando a edição não a traz.
//
// A senha não volta no GET, então o formulário reenvia a configuração sem
// ela; sem isto, salvar qualquer outro campo apagaria a credencial. O
// monitor guardado é o que a guarda de escopo já leu e conferiu.
func (a *API) carrySecrets(w http.ResponseWriter, r *http.Request, m *domain.Monitor) bool {
	cfg, err := checker.CarrySecrets(*m, monitorFrom(r))
	if err != nil {
		writeFieldError(w, "config", err.Error())
		return false
	}
	m.Config = cfg
	return true
}

// audienceOf diz para quem a configuração sai nesta requisição. Criação e
// edição nem perguntam: as rotas já são só de administrador.
func audienceOf(r *http.Request) checker.Audience {
	if u, ok := userFrom(r.Context()); ok && u.Role.CanWrite() {
		return checker.Admins
	}
	return checker.Readers
}

// pushTokenFree recusa o token de push que já pertence a outro monitor.
//
// O índice único do banco é a garantia; esta consulta existe para o erro
// apontar o campo da configuração, onde o administrador sabe o que
// corrigir, em vez de um conflito genérico.
func (a *API) pushTokenFree(w http.ResponseWriter, r *http.Request, m domain.Monitor) bool {
	if m.PushToken == "" {
		return true
	}
	owner, err := a.store.Monitors().FindByPushToken(r.Context(), m.PushToken)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return true
	case err != nil:
		writeStoreError(w, err)
		return false
	case owner.ID != m.ID:
		writeFieldError(w, "config", "este token já pertence a outro monitor; gere um novo")
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
