package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Grupos, escopo e as telas que dependem deles.

type grupoResp struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ParentID *int64 `json:"parent_id"`
	Path     string `json:"path"`
}

func TestGroupCreateDerivesSlugAndPath(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "Produção", &rpa)

	resp := s.do(t, http.MethodGet, "/api/v1/groups/"+itoa(prod), nil)
	assertStatus(t, resp, http.StatusOK)
	g := decode[grupoResp](t, resp)

	// O caminho é o que a tela mostra ("RPA / Produção"); devolvê-lo
	// pronto evita que cada cliente remonte a árvore para escrever um
	// título.
	if g.Slug != "producao" || g.Path != "RPA / Produção" || g.ParentID == nil || *g.ParentID != rpa {
		t.Fatalf("grupo = %+v", g)
	}
}

func TestGroupRefusesThirdLevel(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)

	resp := s.do(t, http.MethodPost, "/api/v1/groups", map[string]any{"name": "fundo", "parent_id": prod})
	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"parent_id"`) {
		t.Error("o erro não apontou parent_id")
	}
}

// Só folha recebe monitor. Um sistema com ambientes e monitores soltos ao
// lado deles teria dois tipos de filho, e "como está o RPA?" deixaria de
// ter uma resposta só.
func TestMonitorMustLiveInALeafGroup(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	s.criarGrupo(t, "prod", &rpa)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", monitorNoGrupo("bff", &rpa))
	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"group_id"`) {
		t.Error("o erro não apontou group_id")
	}

	inexistente := int64(999)
	resp = s.do(t, http.MethodPost, "/api/v1/monitors", monitorNoGrupo("bff", &inexistente))
	assertStatus(t, resp, http.StatusBadRequest)
}

// Criar ambiente num grupo que já tem monitores deixaria esses monitores
// num grupo que não é mais folha.
func TestGroupWithMonitorsCannotGetChildren(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	avulso := s.criarGrupo(t, "Avulso", nil)
	s.createMonitor(t, monitorNoGrupo("api", &avulso))

	resp := s.do(t, http.MethodPost, "/api/v1/groups", map[string]any{"name": "prod", "parent_id": avulso})
	assertStatus(t, resp, http.StatusConflict)
}

func TestGroupDeleteRefusesWhileInUse(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	g := s.criarGrupo(t, "RPA", nil)
	id := s.createMonitor(t, monitorNoGrupo("api", &g))

	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/groups/"+itoa(g), nil), http.StatusConflict)

	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/monitors/"+itoa(id), nil), http.StatusNoContent)
	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/groups/"+itoa(g), nil), http.StatusNoContent)
}

func TestMonitorNameRepeatsAcrossGroups(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)
	hml := s.criarGrupo(t, "hml", &rpa)

	s.createMonitor(t, monitorNoGrupo("bff", &prod))
	id := s.createMonitor(t, monitorNoGrupo("bff", &hml))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), `"group_id":`+itoa(hml)) {
		t.Error("a resposta do monitor não diz em que grupo ele está")
	}
}

func TestMonitorListFiltersByGroupIncludingChildren(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)
	ret := s.criarGrupo(t, "Retaguarda", nil)
	s.createMonitor(t, monitorNoGrupo("bff", &prod))
	s.createMonitor(t, monitorNoGrupo("erp", &ret))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors?group_id="+itoa(rpa), nil)
	assertStatus(t, resp, http.StatusOK)
	corpo := readBody(t, resp)
	if !strings.Contains(corpo, `"bff"`) || strings.Contains(corpo, `"erp"`) {
		t.Fatalf("filtro pelo sistema divergiu: %s", corpo)
	}
}

// ---------- anti-escalonamento ----------

// Quem só tem a Retaguarda não sai do próprio escopo por nenhum caminho
// de escrita: nem criando conta maior, nem movendo monitor, nem
// pendurando canal — nem no próprio grupo.
func TestScopedAdminCannotEscalate(t *testing.T) {
	c := novoCenarioEscopo(t)
	adm := c.s.como(c.retAdmin)

	casos := []struct {
		nome         string
		metodo, rota string
		corpo        map[string]any
		quer         int
	}{
		{"conta com todos os grupos", http.MethodPost, "/api/v1/users", map[string]any{
			"username": "nova", "password": "senha-de-teste-12345", "role": "viewer",
			"scope": map[string]any{"all_groups": true},
		}, http.StatusForbidden},
		{"conta sem escopo dito, que é todos os grupos", http.MethodPost, "/api/v1/users", map[string]any{
			"username": "nova", "password": "senha-de-teste-12345", "role": "viewer",
		}, http.StatusForbidden},
		{"conta com grupo dos outros", http.MethodPost, "/api/v1/users", map[string]any{
			"username": "nova", "password": "senha-de-teste-12345", "role": "viewer",
			"scope": map[string]any{"all_groups": false, "group_ids": []int64{c.ret, c.rpa}},
		}, http.StatusBadRequest},
		{"alargar a própria conta", http.MethodPut, "/api/v1/users/" + itoa(c.contaRestrita), map[string]any{
			"role": "admin", "scope": map[string]any{"all_groups": true},
		}, http.StatusForbidden},
		{"criar monitor no grupo dos outros", http.MethodPost, "/api/v1/monitors",
			monitorNoGrupo("invasor", &c.rpaProd), http.StatusBadRequest},
		{"criar monitor sem grupo", http.MethodPost, "/api/v1/monitors",
			monitorNoGrupo("invasor", nil), http.StatusBadRequest},
		{"mover monitor para o grupo dos outros", http.MethodPut, "/api/v1/monitors/" + itoa(c.dentro),
			monitorNoGrupo("bff", &c.rpaProd), http.StatusBadRequest},
		{"mover monitor para sem grupo", http.MethodPut, "/api/v1/monitors/" + itoa(c.dentro),
			monitorNoGrupo("bff", nil), http.StatusBadRequest},
		{"ambiente no sistema dos outros", http.MethodPost, "/api/v1/groups",
			map[string]any{"name": "dr", "parent_id": c.rpa}, http.StatusBadRequest},
		{"grupo de topo", http.MethodPost, "/api/v1/groups",
			map[string]any{"name": "Novo"}, http.StatusForbidden},
		{"levar ambiente para o sistema dos outros", http.MethodPut, "/api/v1/groups/" + itoa(c.retProd),
			map[string]any{"name": "prod", "parent_id": c.rpa}, http.StatusBadRequest},
		{"tirar ambiente para o topo", http.MethodPut, "/api/v1/groups/" + itoa(c.retProd),
			map[string]any{"name": "prod"}, http.StatusForbidden},
		// O canal é de todo mundo. A guarda olhava o monitor ou o grupo do
		// caminho e nunca o canal, e assim o pager da diretoria ia parar
		// no grupo da Retaguarda: cada queda dela acordaria quem não tem
		// nada com isso, e desvincular silenciaria o aviso dos outros.
		{"vincular canal ao próprio grupo", http.MethodPut,
			"/api/v1/groups/" + itoa(c.retProd) + "/channels/" + itoa(c.canal), nil, http.StatusForbidden},
		{"vincular canal ao próprio monitor", http.MethodPut,
			"/api/v1/monitors/" + itoa(c.dentro) + "/channels/" + itoa(c.canal), nil, http.StatusForbidden},
		{"desvincular canal do próprio monitor", http.MethodDelete,
			"/api/v1/monitors/" + itoa(c.dentro) + "/channels/" + itoa(c.canal), nil, http.StatusForbidden},
	}

	for _, caso := range casos {
		t.Run(caso.nome, func(t *testing.T) {
			resp := adm.do(t, caso.metodo, caso.rota, caso.corpo)
			assertStatus(t, resp, caso.quer)
		})
	}

	// O positivo: dentro do escopo, pode.
	assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/groups",
		map[string]any{"name": "dr", "parent_id": c.ret}), http.StatusCreated)
	assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "colega", "password": "senha-de-teste-12345", "role": "viewer",
		"scope": map[string]any{"all_groups": false, "group_ids": []int64{c.retProd}},
	}), http.StatusCreated)
}

// O escopo da conta aparece em /auth/me, para a interface saber o que
// oferecer sem tentar e errar.
func TestMeShowsTheScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	resp := c.s.como(c.retViewer).do(t, http.MethodGet, "/api/v1/auth/me", nil)
	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), `"scope":{"all_groups":false,"group_ids":[`+itoa(c.ret)+`]}`) {
		t.Error("/auth/me não mostrou o escopo")
	}
}

// ---------- resumo, problemas e eventos ----------

type resumoGrupo struct {
	GroupID  *int64 `json:"group_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Status   string `json:"status"`
	Total    int    `json:"total"`
	Up       int    `json:"up"`
	Degraded int    `json:"degraded"`
	Down     int    `json:"down"`
	Unknown  int    `json:"unknown"`
	Paused   int    `json:"paused"`
	Problems int    `json:"problems"`
}

func (s *server) estado(t *testing.T, id int64, st domain.Status, desde time.Time) {
	t.Helper()
	if err := s.store.States().Save(context.Background(), id, domain.MonitorState{
		Status: st, Candidate: st, Since: desde,
	}); err != nil {
		t.Fatal(err)
	}
}

// O cartão do sistema soma os ambientes e mostra o pior estado deles; o
// pausado não conta como problema, porque pausar é decisão de alguém.
func TestGroupSummaryAggregatesTheWorstState(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)
	hml := s.criarGrupo(t, "hml", &rpa)

	fora := s.createMonitor(t, monitorNoGrupo("bff", &prod))
	ok := s.createMonitor(t, monitorNoGrupo("worker", &prod))
	s.createMonitor(t, monitorNoGrupo("bff", &hml)) // nunca medido
	pausado := monitorNoGrupo("velho", &hml)
	pausado["enabled"] = false
	s.createMonitor(t, pausado)
	solto := s.createMonitor(t, monitorNoGrupo("solto", nil))

	s.estado(t, fora, domain.StatusDown, epoch)
	s.estado(t, ok, domain.StatusUp, epoch)
	s.estado(t, solto, domain.StatusUp, epoch)

	resp := s.do(t, http.MethodGet, "/api/v1/groups/summary", nil)
	assertStatus(t, resp, http.StatusOK)
	itens := decode[struct {
		Items []resumoGrupo `json:"items"`
	}](t, resp).Items

	porNome := map[string]resumoGrupo{}
	for _, i := range itens {
		porNome[i.Path] = i
	}

	if g := porNome["RPA"]; g.Status != "down" || g.Total != 4 || g.Down != 1 || g.Up != 1 ||
		g.Unknown != 1 || g.Paused != 1 || g.Problems != 2 {
		t.Errorf("RPA = %+v", g)
	}
	if g := porNome["RPA / hml"]; g.Status != "unknown" || g.Problems != 1 {
		t.Errorf("RPA / hml = %+v", g)
	}
	if g := porNome["Sem grupo"]; g.GroupID != nil || g.Status != "up" || g.Total != 1 {
		t.Errorf("Sem grupo = %+v", g)
	}
}

type problema struct {
	MonitorID int64      `json:"monitor_id"`
	Name      string     `json:"name"`
	GroupID   *int64     `json:"group_id"`
	GroupPath string     `json:"group_path"`
	Status    string     `json:"status"`
	Since     time.Time  `json:"since"`
	Cause     string     `json:"cause"`
	LastCheck *time.Time `json:"last_check_at"`
}

// "Problemas agora" põe o que está fora do ar antes do degradado, e este
// antes do que parou de ser medido — a ordem em que alguém deve olhar.
func TestProblemsListsWhatIsWrongNow(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	ctx := context.Background()

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)

	lento := s.createMonitor(t, monitorNoGrupo("lento", &prod))
	fora := s.createMonitor(t, monitorNoGrupo("bff", &prod))
	semSinal := s.createMonitor(t, monitorNoGrupo("worker", &prod))
	bem := s.createMonitor(t, monitorNoGrupo("ok", &prod))
	pausado := monitorNoGrupo("pausado", &prod)
	pausado["enabled"] = false
	pausadoID := s.createMonitor(t, pausado)

	s.estado(t, lento, domain.StatusDegraded, epoch.Add(-5*time.Minute))
	s.estado(t, fora, domain.StatusDown, epoch.Add(-12*time.Minute))
	s.estado(t, semSinal, domain.StatusUnknown, epoch.Add(-7*time.Minute))
	s.estado(t, bem, domain.StatusUp, epoch)
	s.estado(t, pausadoID, domain.StatusDown, epoch)
	if err := s.store.WriteHeartbeats(ctx, []domain.Heartbeat{{
		MonitorID: fora, Timestamp: epoch.Add(-time.Minute), Status: domain.StatusDown,
		Message: "connection refused", ProbeID: "local",
	}}); err != nil {
		t.Fatal(err)
	}

	resp := s.do(t, http.MethodGet, "/api/v1/problems", nil)
	assertStatus(t, resp, http.StatusOK)
	itens := decode[struct {
		Items []problema `json:"items"`
	}](t, resp).Items

	if len(itens) != 3 {
		t.Fatalf("problemas = %+v, want 3 (sem o pausado e sem o que está no ar)", itens)
	}
	if itens[0].MonitorID != fora || itens[1].MonitorID != lento || itens[2].MonitorID != semSinal {
		t.Errorf("ordem = %v, %v, %v", itens[0].Name, itens[1].Name, itens[2].Name)
	}
	p := itens[0]
	if p.GroupPath != "RPA / prod" || p.Cause != "connection refused" ||
		!p.Since.Equal(epoch.Add(-12*time.Minute)) || p.LastCheck == nil {
		t.Errorf("problema = %+v", p)
	}
}

type evento struct {
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Status  string    `json:"status"`
	From    string    `json:"from"`
	Message string    `json:"message"`
}

type paginaEventos struct {
	Items      []evento   `json:"items"`
	HasMore    bool       `json:"has_more"`
	NextBefore *time.Time `json:"next_before"`
}

// A aba de eventos mistura mudanças de estado e batidas com falha, cada
// uma com a causa, da mais recente para trás.
func TestMonitorEventsMergeChangesAndFailures(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	ctx := context.Background()

	id := s.createMonitor(t, newMonitorBody("api"))
	if err := s.store.WriteHeartbeats(ctx, []domain.Heartbeat{
		{MonitorID: id, Timestamp: epoch.Add(-3 * time.Minute), Status: domain.StatusDown, Message: "timeout", ProbeID: "local"},
		{MonitorID: id, Timestamp: epoch.Add(-2 * time.Minute), Status: domain.StatusUp, ProbeID: "local"},
		{MonitorID: id, Timestamp: epoch.Add(-1 * time.Minute), Status: domain.StatusDegraded, Message: "lento", ProbeID: "local"},
	}); err != nil {
		t.Fatal(err)
	}
	tr := domain.Transition{MonitorID: id, From: domain.StatusUp, To: domain.StatusDown,
		At: epoch.Add(-150 * time.Second), Cause: "timeout"}
	if err := s.store.Transitions().Record(ctx, &tr); err != nil {
		t.Fatal(err)
	}

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/events", nil)
	assertStatus(t, resp, http.StatusOK)
	pg := decode[paginaEventos](t, resp)

	if len(pg.Items) != 3 {
		t.Fatalf("eventos = %+v, want 2 falhas e 1 mudança (a batida boa fica de fora)", pg.Items)
	}
	if pg.Items[0].Type != "failure" || pg.Items[0].Message != "lento" ||
		pg.Items[1].Type != "state" || pg.Items[1].From != "up" || pg.Items[1].Status != "down" ||
		pg.Items[2].Type != "failure" || pg.Items[2].Message != "timeout" {
		t.Errorf("eventos fora de ordem ou sem causa: %+v", pg.Items)
	}

	resp = s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/events?type=state", nil)
	assertStatus(t, resp, http.StatusOK)
	if pg := decode[paginaEventos](t, resp); len(pg.Items) != 1 || pg.Items[0].Type != "state" {
		t.Errorf("filtro por tipo divergiu: %+v", pg.Items)
	}
}

func TestMonitorEventsPaginate(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	ctx := context.Background()

	id := s.createMonitor(t, newMonitorBody("api"))
	var hbs []domain.Heartbeat
	for i := 0; i < 5; i++ {
		hbs = append(hbs, domain.Heartbeat{MonitorID: id, Timestamp: epoch.Add(time.Duration(-i) * time.Minute),
			Status: domain.StatusDown, Message: "falha", ProbeID: "local"})
	}
	if err := s.store.WriteHeartbeats(ctx, hbs); err != nil {
		t.Fatal(err)
	}

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/events?limit=2", nil)
	assertStatus(t, resp, http.StatusOK)
	primeira := decode[paginaEventos](t, resp)
	if len(primeira.Items) != 2 || !primeira.HasMore || primeira.NextBefore == nil {
		t.Fatalf("primeira página = %+v", primeira)
	}

	resp = s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id)+"/events?limit=10&before="+
		primeira.NextBefore.Format(time.RFC3339Nano), nil)
	assertStatus(t, resp, http.StatusOK)
	segunda := decode[paginaEventos](t, resp)
	if len(segunda.Items) != 3 || segunda.HasMore {
		t.Fatalf("segunda página = %+v", segunda)
	}
	if !segunda.Items[0].At.Before(primeira.Items[1].At) {
		t.Error("a segunda página repetiu a primeira")
	}
}

// ---------- aviso por grupo ----------

func TestGroupChannelsCanBeLinked(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	g := s.criarGrupo(t, "RPA", nil)
	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "time", "type": "webhook", "config": map[string]any{"url": "https://exemplo.com/hook"},
	})
	assertStatus(t, resp, http.StatusCreated)
	canal := decode[idResp](t, resp).ID

	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/groups/"+itoa(g)+"/channels/"+itoa(canal), nil), http.StatusNoContent)

	resp = s.do(t, http.MethodGet, "/api/v1/groups/"+itoa(g)+"/channels", nil)
	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), `"name":"time"`) {
		t.Error("o canal vinculado não apareceu no grupo")
	}

	assertStatus(t, s.do(t, http.MethodPut, "/api/v1/groups/"+itoa(g)+"/channels/999", nil), http.StatusNotFound)
	assertStatus(t, s.do(t, http.MethodDelete, "/api/v1/groups/"+itoa(g)+"/channels/"+itoa(canal), nil), http.StatusNoContent)
}

// Vincular um canal que não existe é 404, como no grupo. Antes o pedido
// chegava ao banco e a chave estrangeira respondia com erro interno, que
// não diz a quem chama o que corrigir.
func TestLinkingUnknownChannelIsNotFound(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.createMonitor(t, newMonitorBody("api"))
	g := s.criarGrupo(t, "RPA", nil)
	for _, rota := range []string{
		"/api/v1/monitors/" + itoa(id) + "/channels/999",
		"/api/v1/groups/" + itoa(g) + "/channels/999",
	} {
		for _, metodo := range []string{http.MethodPut, http.MethodDelete} {
			t.Run(metodo+" "+rota, func(t *testing.T) {
				assertStatus(t, s.do(t, metodo, rota, nil), http.StatusNotFound)
			})
		}
	}
}
