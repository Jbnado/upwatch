package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
	// Targets, quando vem, troca todos os vínculos do canal na mesma
	// gravação. Ausente na edição mantém os vínculos atuais.
	Targets *targetsBody `json:"targets,omitempty"`
}

// targetsBody é onde o canal está ligado direto. Grupo vale para os
// monitores dele e, no sistema, para os ambientes, inclusive os criados
// depois; monitor vale só para ele.
type targetsBody struct {
	GroupIDs   []int64 `json:"group_ids"`
	MonitorIDs []int64 `json:"monitor_ids"`
}

func targetsFrom(t store.ChannelTargets) targetsBody {
	b := targetsBody{GroupIDs: t.GroupIDs, MonitorIDs: t.MonitorIDs}
	if b.GroupIDs == nil {
		b.GroupIDs = []int64{}
	}
	if b.MonitorIDs == nil {
		b.MonitorIDs = []int64{}
	}
	return b
}

// channelItem é o canal com onde ele está ligado, para a lista dizer
// "3 grupos, 2 monitores" sem uma consulta por canal. Campos explícitos,
// e não o domain.Channel embutido: o MarshalJSON dele, promovido, engoliria
// os vínculos.
type channelItem struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	Type      string      `json:"type"`
	Enabled   bool        `json:"enabled"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Targets   targetsBody `json:"targets"`
}

func newChannelItem(c domain.Channel, t store.ChannelTargets) channelItem {
	return channelItem{
		ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Targets: targetsFrom(t),
	}
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

// handleListChannels devolve os canais que a conta enxerga (ver
// visibleChannels).
func (a *API) handleListChannels(w http.ResponseWriter, r *http.Request) {
	canais, err := a.visibleChannels(r.Context(), viewerFrom(r))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	v := viewerFrom(r)
	alvos, err := a.store.Channels().Targets(r.Context(), v.monitorSelection())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := make([]channelItem, 0, len(canais))
	for _, c := range canais {
		items = append(items, newChannelItem(c, alvos[c.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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

	var alvos store.ChannelTargets
	if req.Targets != nil {
		var ok bool
		if alvos, ok = a.resolveTargets(w, r, *req.Targets); !ok {
			return
		}
	}
	if err := a.store.Channels().CreateWithTargets(r.Context(), &c, alvos); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newChannelItem(c, alvos))
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
	alvos, err := a.store.Channels().Targets(r.Context(), viewerFrom(r).monitorSelection())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !canEditChannels(r) {
		writeJSON(w, http.StatusOK, newChannelItem(c, alvos[c.ID]))
		return
	}
	writeJSON(w, http.StatusOK, channelEditResponse{
		ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled,
		Config:    notifier.RedactConfig(c.Type, c.Config),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Targets: targetsFrom(alvos[c.ID]),
	})
}

// channelEditResponse é o canal com a configuração que o formulário de
// edição precisa, sem credencial (ver notifier.RedactConfig).
type channelEditResponse struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	Enabled   bool            `json:"enabled"`
	Config    json.RawMessage `json:"config,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Targets   targetsBody     `json:"targets"`
}

// canEditChannels diz se a configuração do canal volta nesta leitura: só
// para quem pode salvá-la. Mesmo sem credencial, ela traz destinatários,
// tópico e servidores, que não são da conta de quem só lê.
func canEditChannels(r *http.Request) bool {
	u, ok := userFrom(r.Context())
	return ok && u.Role.CanWrite() && viewerFrom(r).access.All()
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

	guardado, err := a.store.Channels().Get(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A credencial não volta no GET, então a edição chega sem ela.
	c.Config, err = notifier.CarrySecrets(c.Type, c.Config, guardado.Type, guardado.Config)
	if err != nil {
		writeFieldError(w, "config", err.Error())
		return
	}

	if !validateChannel(w, c) {
		return
	}

	if req.Targets == nil {
		err = a.store.Channels().Update(r.Context(), c)
	} else {
		alvos, ok := a.resolveTargets(w, r, *req.Targets)
		if !ok {
			return
		}
		err = a.store.Channels().UpdateWithTargets(r.Context(), c, alvos)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleSetChannelTargets troca, de uma vez, todos os grupos e monitores
// ligados ao canal.
//
// Uma requisição para o conjunto, e não uma por vínculo: com uma por
// vínculo, a falha no meio deixaria o canal com metade do que foi pedido,
// e a tela não teria como dizer qual metade.
func (a *API) handleSetChannelTargets(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req targetsBody
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.channelExists(w, r, id) {
		return
	}
	alvos, ok := a.resolveTargets(w, r, req)
	if !ok {
		return
	}
	if err := a.store.Channels().SetTargets(r.Context(), id, alvos); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, targetsFrom(alvos))
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
	if !a.channelExists(w, r, channelID) {
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
	if !a.channelExists(w, r, channelID) {
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
//
// Pagina para trás, como a linha do tempo do monitor: cursor (o
// next_cursor da página anterior) traz os incidentes mais antigos que o
// último visto. O cursor é opaco para quem consome; hoje é o id.
func (a *API) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	antesDe, ok := incidentCursor(w, r)
	if !ok {
		return
	}

	filter := store.IncidentFilter{
		BeforeID:  antesDe,
		Limit:     int(queryInt64(r, "limit", 0)),
		MonitorID: queryInt64(r, "monitor_id", 0),
		OnlyOpen:  r.URL.Query().Get("open") == "true",
		// Pedir o monitor de fora pelo parâmetro devolve lista vazia, como
		// um monitor sem quedas: nada confirma que ele existe.
		Groups: viewerFrom(r).monitorSelection(),
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
		ultimo := page.Items[len(page.Items)-1].ID
		body["next_cursor"] = strconv.FormatInt(ultimo, 10)
		// Obsoleto, mantido enquanto after_id for aceito: o cliente antigo
		// só conhece este nome.
		body["next_after_id"] = ultimo
	}
	writeJSON(w, http.StatusOK, body)
}

// incidentCursor lê a posição da página de incidentes.
//
// after_id é o nome antigo, aceito como alias obsoleto de cursor para não
// quebrar quem já integra, com o sentido corrigido: mais antigos que o
// id. Antes ele cortava para o lado errado e devolvia sempre a primeira
// página. Os dois juntos e diferentes são 400, porque escolher um em
// silêncio devolveria a página que o cliente não pediu.
func incidentCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	var antesDe int64
	for _, nome := range []string{"cursor", "after_id"} {
		raw := r.URL.Query().Get(nome)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeFieldError(w, nome, "cursor inválido; use o next_cursor da página anterior")
			return 0, false
		}
		if antesDe != 0 && antesDe != id {
			writeFieldError(w, "after_id", "cursor e after_id apontam posições diferentes; use só cursor")
			return 0, false
		}
		antesDe = id
	}
	return antesDe, true
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

// resolveTargets confere cada grupo e monitor como as rotas de vínculo
// avulso conferem: o que não existe ou está fora do escopo é 404, com o id
// na mensagem para a tela dizer qual. Repetidos contam uma vez.
func (a *API) resolveTargets(w http.ResponseWriter, r *http.Request, b targetsBody) (store.ChannelTargets, bool) {
	v := viewerFrom(r)
	grupos, ok := idsUnicos(w, "targets.group_ids", b.GroupIDs)
	if !ok {
		return store.ChannelTargets{}, false
	}
	monitores, ok := idsUnicos(w, "targets.monitor_ids", b.MonitorIDs)
	if !ok {
		return store.ChannelTargets{}, false
	}

	for _, id := range grupos {
		if !v.access.Group(id) {
			writeError(w, http.StatusNotFound, codeNotFound, "grupo %d não encontrado", id)
			return store.ChannelTargets{}, false
		}
	}
	for _, id := range monitores {
		m, err := a.store.Monitors().Get(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !v.sees(m)) {
			writeError(w, http.StatusNotFound, codeNotFound, "monitor %d não encontrado", id)
			return store.ChannelTargets{}, false
		}
		if err != nil {
			writeStoreError(w, err)
			return store.ChannelTargets{}, false
		}
	}
	return store.ChannelTargets{GroupIDs: grupos, MonitorIDs: monitores}, true
}

// idsUnicos devolve os ids em ordem, sem repetidos, recusando os inválidos.
func idsUnicos(w http.ResponseWriter, campo string, ids []int64) ([]int64, bool) {
	vistos := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			writeFieldError(w, campo, "identificador inválido")
			return nil, false
		}
		if !vistos[id] {
			vistos[id] = true
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, true
}

// channelExists responde 404 ao canal que não existe.
//
// Vincular um id errado criaria um vínculo que nunca avisa, e ninguém
// perceberia até a queda; desvincular um id errado responderia "feito"
// sobre nada.
func (a *API) channelExists(w http.ResponseWriter, r *http.Request, id int64) bool {
	if _, err := a.store.Channels().Get(r.Context(), id); err != nil {
		writeStoreError(w, err)
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
