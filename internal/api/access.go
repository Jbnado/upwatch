package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// Escopo de acesso aplicado à API.
//
// Tudo que decide "esta conta alcança isto?" mora neste arquivo, e as
// perguntas em si são respondidas por domain.Access. Os handlers não
// decidem: recebem o monitor já conferido pela guarda do caminho, ou
// pedem aqui a seleção de grupos para filtrar uma listagem. Dois lugares
// decidindo a mesma coisa acabam divergindo, e em acesso a divergência é
// vazamento.
//
// Fora do escopo é 404, nunca 403. Responder "proibido" para o monitor de
// outro grupo e "não encontrado" para um id inventado ensinaria a quem
// chuta ids quais existem.

const (
	viewerContextKey contextKey = iota + 100
	monitorContextKey
)

// viewer é a conta da requisição com o escopo já resolvido.
type viewer struct {
	user   domain.User
	access domain.Access
	groups domain.GroupTree
}

// resolveViewer resolve o escopo contra a árvore de grupos atual.
//
// Na hora, e não no login: um ambiente criado depois sob um sistema
// concedido entra no escopo sem ninguém atualizar a conta, e uma
// concessão retirada vale na requisição seguinte.
func (a *API) resolveViewer(ctx context.Context, u domain.User) (viewer, error) {
	grupos, err := a.store.Groups().List(ctx)
	if err != nil {
		return viewer{}, err
	}
	tree := domain.NewGroupTree(grupos)
	return viewer{user: u, access: domain.NewAccess(u.Scope, tree), groups: tree}, nil
}

// viewerFrom devolve o escopo resolvido pela guarda.
func viewerFrom(r *http.Request) viewer {
	v, _ := r.Context().Value(viewerContextKey).(viewer)
	return v
}

// monitorFrom devolve o monitor do caminho, já conferido pela guarda.
func monitorFrom(r *http.Request) domain.Monitor {
	m, _ := r.Context().Value(monitorContextKey).(domain.Monitor)
	return m
}

// withViewer resolve o escopo da conta autenticada para o resto da
// requisição.
func (a *API) withViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := a.resolveViewer(r.Context(), mustUser(r))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerContextKey, v)))
	})
}

// scopePolicy é como uma rota se relaciona com o escopo.
//
// Cada rota declara a sua ao ser registrada (ver routeTable). A guarda não
// adivinha pelo caminho: classificar por prefixo deixava passar a rota
// nova que caísse num prefixo que ninguém previu, e em acesso "na dúvida,
// deixa" é vazamento esperando alguém criar a rota certa.
//
// As marcas se combinam: vincular canal a monitor é policyFullScope com
// policyMonitor.
type scopePolicy uint

const (
	// policyPublic atende sem sessão; as regras dela moram no handler
	// (página pública, push, métricas).
	policyPublic scopePolicy = 1 << iota
	// policyAccount diz respeito à própria conta ou a nada da instalação:
	// sessão, senha, tokens, tipos de canal.
	policyAccount
	// policyFiltered devolve coleção, recortada pelo handler com o viewer.
	policyFiltered
	// policyPlacement cria algo num grupo; o handler confere o destino.
	policyPlacement
	// policyStream é o fluxo de eventos, recortado evento a evento.
	policyStream
	// policyFullScope só atende quem alcança tudo. A recusa vem antes de
	// olhar o recurso, para a resposta ser a mesma dentro e fora do
	// escopo.
	policyFullScope
	// policyMonitor: {id} é um monitor, que precisa estar no escopo.
	policyMonitor
	// policyGroup: {id} é um grupo, que precisa estar no escopo.
	policyGroup
	// policyUser: {id} é uma conta, que não pode enxergar mais que quem
	// pede.
	policyUser
	// policyMonitorParam: {monitorID} é um monitor, que precisa estar no
	// escopo.
	policyMonitorParam
	// policyChannel: {id} é um canal, que precisa avisar sobre algo do
	// escopo.
	policyChannel
)

// scopeGuard aplica a política declarada pela rota.
//
// Middleware do grupo de rotas, como requireAdmin. Rota sem política não
// é atendida: o roteador nem sobe assim (checkPolicies), e esta recusa é
// a segunda trava, para o caso de alguém montar rotas por outro caminho.
func (a *API) scopeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		rota := r.Method + " " + chi.RouteContext(ctx).RoutePattern()
		p, ok := a.policies[rota]
		if !ok || p == 0 || p&policyPublic != 0 {
			slog.Error("api: rota autenticada sem política de escopo", "rota", rota)
			writeError(w, http.StatusInternalServerError, codeInternal, "rota sem política de escopo")
			return
		}

		v := viewerFrom(r)
		if p&policyFullScope != 0 && !v.access.All() {
			writeError(w, http.StatusForbidden, codeForbidden,
				"só quem alcança todos os grupos pode fazer isso")
			return
		}

		var err error
		if p&policyMonitor != 0 {
			ctx, err = a.guardMonitor(ctx, v, chi.URLParam(r, "id"))
		}
		if err == nil && p&policyGroup != 0 {
			err = guardGroup(v, chi.URLParam(r, "id"))
		}
		if err == nil && p&policyUser != 0 {
			err = a.guardUser(ctx, v, chi.URLParam(r, "id"))
		}
		if err == nil && p&policyChannel != 0 {
			err = a.guardChannel(ctx, v, chi.URLParam(r, "id"))
		}
		if err == nil && p&policyMonitorParam != 0 {
			ctx, err = a.guardMonitor(ctx, v, chi.URLParam(r, "monitorID"))
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// routeTable registra rotas junto com a política de escopo de cada uma.
//
// Registrar e declarar são um gesto só: não existe forma de pôr uma rota
// no roteador por aqui sem dizer como ela respeita o escopo.
type routeTable struct {
	r        chi.Router
	prefix   string
	policies map[string]scopePolicy
}

func (t routeTable) handle(method, pattern string, p scopePolicy, h http.HandlerFunc) {
	t.policies[method+" "+t.prefix+pattern] = p
	t.r.Method(method, pattern, h)
}

// on devolve a mesma tabela registrando num grupo de rotas.
func (t routeTable) on(r chi.Router) routeTable {
	t.r = r
	return t
}

// checkPolicies recusa o roteador que tenha rota sem política declarada.
func checkPolicies(routes chi.Routes, policies map[string]scopePolicy) error {
	var faltando []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/*")
		if route != "/" {
			route = strings.TrimSuffix(route, "/")
		}
		chave := method + " " + route
		if policies[chave] == 0 {
			faltando = append(faltando, chave)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(faltando) > 0 {
		sort.Strings(faltando)
		return fmt.Errorf("api: rotas sem política de escopo: %s", strings.Join(faltando, ", "))
	}
	return nil
}

// errOutOfScope é o "não encontrado" de quem está fora do escopo. Mesma
// resposta de um id que não existe.
var errOutOfScope = store.ErrNotFound

// parseParam lê o id do caminho. Id malformado passa adiante, para o
// handler responder 400 do jeito de sempre.
func parseParam(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

func (a *API) guardMonitor(ctx context.Context, v viewer, raw string) (context.Context, error) {
	id, ok := parseParam(raw)
	if !ok {
		return ctx, nil
	}
	m, err := a.store.Monitors().Get(ctx, id)
	if err != nil {
		return ctx, err
	}
	if !v.access.Monitor(m) {
		return ctx, errOutOfScope
	}
	return context.WithValue(ctx, monitorContextKey, m), nil
}

func guardGroup(v viewer, raw string) error {
	id, ok := parseParam(raw)
	if !ok {
		return nil
	}
	if !v.access.Group(id) {
		return errOutOfScope
	}
	return nil
}

// guardChannel esconde o canal que não avisa sobre nada do escopo.
func (a *API) guardChannel(ctx context.Context, v viewer, raw string) error {
	id, ok := parseParam(raw)
	if !ok || v.access.All() {
		return nil
	}
	canais, err := a.visibleChannels(ctx, v)
	if err != nil {
		return err
	}
	for _, c := range canais {
		if c.ID == id {
			return nil
		}
	}
	return errOutOfScope
}

// visibleChannels devolve os canais que a conta enxerga.
//
// Para quem tem escopo, só os vinculados a monitores ou grupos dele. O
// canal é global, mas o nome e o destino de um canal que só avisa sobre
// os outros dizem quem existe fora — "pager-diretoria" já é informação.
func (a *API) visibleChannels(ctx context.Context, v viewer) ([]domain.Channel, error) {
	if v.access.All() {
		return a.store.Channels().List(ctx)
	}
	return a.store.Channels().ListFor(ctx, *v.monitorSelection())
}

// guardUser esconde a conta que enxerga mais que quem pede: alterá-la
// seria mexer em quem está acima dele.
func (a *API) guardUser(ctx context.Context, v viewer, raw string) error {
	id, ok := parseParam(raw)
	if !ok {
		return nil
	}
	u, err := a.store.Users().Get(ctx, id)
	if err != nil {
		return err
	}
	if !v.access.Grants(u.Scope) {
		return errOutOfScope
	}
	return nil
}

// monitorSelection é o filtro de listagem do escopo. Nulo para quem
// alcança tudo.
func (v viewer) monitorSelection() *store.GroupSelection {
	if v.access.All() {
		return nil
	}
	return &store.GroupSelection{IDs: v.access.GroupIDs()}
}

// selectGroup restringe a listagem a um grupo pedido, com os ambientes
// dele, sem sair do escopo.
func (v viewer) selectGroup(groupID int64) *store.GroupSelection {
	ids := []int64{}
	for _, id := range append([]int64{groupID}, v.groups.Children(groupID)...) {
		if v.access.Group(id) {
			ids = append(ids, id)
		}
	}
	return &store.GroupSelection{IDs: ids}
}

// sees informa se o monitor está no escopo.
func (v viewer) sees(m domain.Monitor) bool { return v.access.Monitor(m) }

// visibleMonitorIDs devolve os ids alcançados, para filtrar respostas que
// citam monitores por id (componentes de página, relatos). Nulo para quem
// alcança tudo.
func (a *API) visibleMonitorIDs(ctx context.Context, v viewer) (map[int64]bool, error) {
	if v.access.All() {
		return nil, nil
	}
	monitores, err := a.listAllMonitors(ctx, store.MonitorFilter{Groups: v.monitorSelection()})
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool, len(monitores))
	for _, m := range monitores {
		ids[m.ID] = true
	}
	return ids, nil
}

// listAllMonitors percorre todas as páginas. Para resumos que precisam do
// conjunto inteiro; listagem para tela continua paginada.
func (a *API) listAllMonitors(ctx context.Context, f store.MonitorFilter) ([]domain.Monitor, error) {
	f.Page = store.PageFilter{Limit: store.MaxPageSize}
	var out []domain.Monitor
	for {
		page, err := a.store.Monitors().List(ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if !page.HasMore || len(page.Items) == 0 {
			return out, nil
		}
		f.Page.AfterID = page.Items[len(page.Items)-1].ID
	}
}

// errScopeTooWide é o pedido de conceder mais do que se tem.
var errScopeTooWide = errors.New("api: escopo maior que o de quem concede")

// checkMonitorPlacement decide se a conta pode pôr um monitor no grupo.
//
// Grupo fora do escopo responde como grupo inexistente. "Sem grupo" só é
// destino para quem alcança tudo: para quem tem escopo, seria tirar o
// monitor do próprio alcance — e do de todo o time dele.
func (v viewer) checkMonitorPlacement(groupID *int64) error {
	if groupID == nil {
		if !v.access.All() {
			return &domain.ValidationError{Field: "group_id", Msg: "escolha um dos seus grupos"}
		}
		return nil
	}
	if !v.access.Group(*groupID) {
		return &domain.ValidationError{Field: "group_id", Msg: "grupo não encontrado"}
	}
	if !v.groups.IsLeaf(*groupID) {
		return &domain.ValidationError{Field: "group_id",
			Msg: "o monitor fica num ambiente; este grupo tem ambientes dentro dele"}
	}
	return nil
}

// checkMonitorParent decide se a conta pode pôr o monitor sob o pai
// (dependência) pedido.
//
// O pai de fora do escopo responde como o inexistente: aceitar um e
// recusar o outro ensinaria a quem chuta ids quais existem, e penduraria
// o monitor numa dependência que a conta nem enxerga.
func (a *API) checkMonitorParent(ctx context.Context, v viewer, parentID *int64) error {
	if parentID == nil {
		return nil
	}
	pai, err := a.store.Monitors().Get(ctx, *parentID)
	if err != nil {
		return err
	}
	if !v.sees(pai) {
		return errOutOfScope
	}
	return nil
}

// checkGroupPlacement decide se a conta pode criar ou mover o grupo para
// onde pediu. anterior é nulo na criação.
//
// Grupo de topo é um sistema novo, fora de qualquer escopo restrito, e só
// quem alcança tudo cria. Levar um ambiente para o topo também, porque o
// tiraria do sistema pelo qual a conta o alcança.
func (v viewer) checkGroupPlacement(g domain.Group, anterior *domain.Group) error {
	if g.ParentID == nil {
		viraTopo := anterior == nil || anterior.ParentID != nil
		if viraTopo && !v.access.All() {
			return errScopeTooWide
		}
	} else if !v.access.Group(*g.ParentID) {
		return &domain.ValidationError{Field: "parent_id", Msg: "grupo pai não encontrado"}
	}
	return v.groups.CheckPlacement(g)
}

// checkGrant decide se a conta pode conceder o escopo a outra.
//
// Conceder todos os grupos sem tê-los é 403; citar grupo fora do próprio
// escopo responde como grupo inexistente.
func (v viewer) checkGrant(s domain.Scope) error {
	if v.access.Grants(s) {
		return nil
	}
	if !s.Restricted {
		return errScopeTooWide
	}
	return &domain.ValidationError{Field: "scope", Msg: "grupo não encontrado"}
}

// writeScopeError traduz a recusa de escalonamento.
func writeScopeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errScopeTooWide) {
		writeError(w, http.StatusForbidden, codeForbidden, "seu escopo não permite isso")
		return
	}
	writeStoreError(w, err)
}
