package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// groupCases cobrem grupos, escopo de conta, aviso por grupo e o histórico
// de mudanças de estado.
func groupCases() []conformanceCase {
	return []conformanceCase{
		{"GroupRoundTrips", testGroupRoundTrips},
		{"GroupGetMissingReturnsNotFound", testGroupGetMissingReturnsNotFound},
		{"GroupSlugIsUniqueAmongSiblings", testGroupSlugIsUniqueAmongSiblings},
		{"GroupUpdatePersistsChanges", testGroupUpdatePersistsChanges},
		{"GroupDeleteRefusesWhileInUse", testGroupDeleteRefusesWhileInUse},
		{"GroupDeleteRemovesEmptyGroup", testGroupDeleteRemovesEmptyGroup},

		{"MonitorRoundTripsGroup", testMonitorRoundTripsGroup},
		{"MonitorRefusesMissingGroup", testMonitorRefusesMissingGroup},
		{"MonitorNameIsUniquePerGroup", testMonitorNameIsUniquePerGroup},
		{"MonitorListFiltersByGroups", testMonitorListFiltersByGroups},
		{"MonitorListEmptySelectionReturnsNothing", testMonitorListEmptySelectionReturnsNothing},
		{"IncidentListFiltersByGroups", testIncidentListFiltersByGroups},

		{"UserScopeDefaultsToAllGroups", testUserScopeDefaultsToAllGroups},
		{"UserScopeRoundTrips", testUserScopeRoundTrips},
		{"UserScopeSurvivesList", testUserScopeSurvivesList},
		{"UserScopeLosesDeletedGroup", testUserScopeLosesDeletedGroup},

		{"ChannelGroupLinkRoundTrips", testChannelGroupLinkRoundTrips},
		{"ChannelLinkToMissingReturnsNotFound", testChannelLinkToMissingReturnsNotFound},
		{"ChannelListForSelectsByLinks", testChannelListForSelectsByLinks},
		{"ChannelDestinationsIncludeGroupAndParent", testChannelDestinationsIncludeGroupAndParent},
		{"ChannelDestinationsCoverFutureMonitors", testChannelDestinationsCoverFutureMonitors},
		{"ChannelDestinationsSkipDisabled", testChannelDestinationsSkipDisabled},
		{"ChannelDestinationsWithoutGroup", testChannelDestinationsWithoutGroup},

		{"TransitionRoundTrips", testTransitionRoundTrips},
		{"TransitionListIsNewestFirstAndPaginates", testTransitionListIsNewestFirstAndPaginates},
		{"TransitionCascadesOnMonitorDelete", testTransitionCascadesOnMonitorDelete},
		{"HeartbeatQueryOnlyFailures", testHeartbeatQueryOnlyFailures},
	}
}

func mustCreateGroup(t *testing.T, s store.Store, name string, parent *int64) domain.Group {
	t.Helper()
	g := domain.Group{Name: name, Slug: domain.Slugify(name), ParentID: parent}
	if err := s.Groups().Create(context.Background(), &g); err != nil {
		t.Fatalf("Groups().Create(%q) returned unexpected error: %v", name, err)
	}
	return g
}

func mustCreateMonitorIn(t *testing.T, s store.Store, name string, groupID *int64) int64 {
	t.Helper()
	m := newMonitor(name)
	m.GroupID = groupID
	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create(%q) returned unexpected error: %v", name, err)
	}
	return m.ID
}

// ---------- grupos ----------

func testGroupRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	prod := mustCreateGroup(t, s, "prod", &rpa.ID)
	if rpa.ID == 0 || prod.ID == 0 || rpa.CreatedAt.IsZero() {
		t.Fatalf("Create did not fill ID and timestamps: %+v %+v", rpa, prod)
	}

	got, err := s.Groups().Get(ctx, prod.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Name != "prod" || got.Slug != "prod" || got.ParentID == nil || *got.ParentID != rpa.ID {
		t.Errorf("group = %+v, want prod under %d", got, rpa.ID)
	}

	all, err := s.Groups().List(ctx)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List returned %d groups, want 2", len(all))
	}
}

func testGroupGetMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	if _, err := s.Groups().Get(context.Background(), 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(999) returned %v, want store.ErrNotFound", err)
	}
}

// "prod" existe em todo sistema; o que não pode é haver duas "prod" no
// mesmo sistema, porque o caminho "RPA / prod" deixaria de apontar um
// grupo só.
func testGroupSlugIsUniqueAmongSiblings(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	ret := mustCreateGroup(t, s, "Retaguarda", nil)
	mustCreateGroup(t, s, "prod", &rpa.ID)
	mustCreateGroup(t, s, "prod", &ret.ID)

	dup := domain.Group{Name: "prod", Slug: "prod", ParentID: &rpa.ID}
	if err := s.Groups().Create(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate sibling returned %v, want store.ErrConflict", err)
	}
	topo := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := s.Groups().Create(ctx, &topo); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate top-level group returned %v, want store.ErrConflict", err)
	}
}

func testGroupUpdatePersistsChanges(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	g.Name, g.Slug = "Robôs", "robos"
	if err := s.Groups().Update(ctx, g); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	got, _ := s.Groups().Get(ctx, g.ID)
	if got.Name != "Robôs" || got.Slug != "robos" {
		t.Errorf("group after Update = %+v", got)
	}

	if err := s.Groups().Update(ctx, domain.Group{ID: 999, Name: "x", Slug: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update(999) returned %v, want store.ErrNotFound", err)
	}
}

// Apagar um grupo com monitores ou ambientes perderia o vínculo deles em
// silêncio. A instalação que reorganiza precisa mover antes de apagar.
func testGroupDeleteRefusesWhileInUse(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	prod := mustCreateGroup(t, s, "prod", &rpa.ID)
	mustCreateMonitorIn(t, s, "bff", &prod.ID)

	if err := s.Groups().Delete(ctx, rpa.ID); !errors.Is(err, store.ErrConflict) {
		t.Errorf("Delete of a group with children returned %v, want store.ErrConflict", err)
	}
	if err := s.Groups().Delete(ctx, prod.ID); !errors.Is(err, store.ErrConflict) {
		t.Errorf("Delete of a group with monitors returned %v, want store.ErrConflict", err)
	}
}

func testGroupDeleteRemovesEmptyGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	canal := mustCreateChannel(t, s, "discord")
	if err := s.Channels().LinkGroup(ctx, g.ID, canal.ID); err != nil {
		t.Fatalf("LinkGroup returned unexpected error: %v", err)
	}

	if err := s.Groups().Delete(ctx, g.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	if _, err := s.Groups().Get(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after Delete returned %v, want store.ErrNotFound", err)
	}
	if err := s.Groups().Delete(ctx, g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Delete returned %v, want store.ErrNotFound", err)
	}
}

// ---------- monitores no grupo ----------

func testMonitorRoundTripsGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	id := mustCreateMonitorIn(t, s, "bff", &g.ID)

	got, _ := s.Monitors().Get(ctx, id)
	if got.GroupID == nil || *got.GroupID != g.ID {
		t.Fatalf("GroupID = %v, want %d", got.GroupID, g.ID)
	}

	// Mover para "Sem grupo" é gravar nulo, não zero: zero apontaria um
	// grupo inexistente.
	got.GroupID = nil
	if err := s.Monitors().Update(ctx, got); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}
	depois, _ := s.Monitors().Get(ctx, id)
	if depois.GroupID != nil {
		t.Errorf("GroupID = %d after moving out, want nil", *depois.GroupID)
	}
}

// Monitor em grupo que não existe é ErrNotFound, e nada é gravado.
//
// É o que sobra de um grupo apagado em paralelo: a API conferiu que ele
// existia, outro pedido o apagou, e a gravação chega depois. No SQLite a
// coluna não tem chave estrangeira (ver a migração), e sem a condição na
// própria instrução o monitor ficava apontando para um grupo que não
// existe — invisível para todo escopo restrito, e fora de "Sem grupo".
func testMonitorRefusesMissingGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	if err := s.Groups().Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}

	m := newMonitor("orfao")
	m.GroupID = &g.ID
	if err := s.Monitors().Create(ctx, &m); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Create in a deleted group returned %v, want store.ErrNotFound", err)
	}
	page, err := s.Monitors().List(ctx, store.MonitorFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("the refused Create left %d monitors behind", len(page.Items))
	}

	id := mustCreateMonitorIn(t, s, "api", nil)
	m, _ = s.Monitors().Get(ctx, id)
	m.GroupID = &g.ID
	if err := s.Monitors().Update(ctx, m); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update into a deleted group returned %v, want store.ErrNotFound", err)
	}
	if got, _ := s.Monitors().Get(ctx, id); got.GroupID != nil {
		t.Errorf("the refused Update moved the monitor to group %d", *got.GroupID)
	}
}

// O nome é curto dentro do grupo ("bff"); repeti-lo em produção e
// homologação é o caso de uso, não um conflito. Repetir dentro do mesmo
// grupo continua sendo.
func testMonitorNameIsUniquePerGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	prod := mustCreateGroup(t, s, "prod", &rpa.ID)
	hml := mustCreateGroup(t, s, "hml", &rpa.ID)

	mustCreateMonitorIn(t, s, "bff", &prod.ID)
	mustCreateMonitorIn(t, s, "bff", &hml.ID)
	mustCreateMonitorIn(t, s, "bff", nil)

	dup := newMonitor("bff")
	dup.GroupID = &prod.ID
	if err := s.Monitors().Create(ctx, &dup); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate name in the same group returned %v, want store.ErrConflict", err)
	}
	semGrupo := newMonitor("bff")
	if err := s.Monitors().Create(ctx, &semGrupo); !errors.Is(err, store.ErrConflict) {
		t.Errorf("duplicate name without group returned %v, want store.ErrConflict", err)
	}
}

func testMonitorListFiltersByGroups(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateGroup(t, s, "A", nil)
	b := mustCreateGroup(t, s, "B", nil)
	emA := mustCreateMonitorIn(t, s, "em-a", &a.ID)
	mustCreateMonitorIn(t, s, "em-b", &b.ID)
	solto := mustCreateMonitorIn(t, s, "solto", nil)

	page, err := s.Monitors().List(ctx, store.MonitorFilter{Groups: &store.GroupSelection{IDs: []int64{a.ID}}})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != emA {
		t.Errorf("List by group A returned %+v, want only em-a", page.Items)
	}

	page, _ = s.Monitors().List(ctx, store.MonitorFilter{
		Groups: &store.GroupSelection{IDs: []int64{a.ID}, Ungrouped: true},
	})
	if len(page.Items) != 2 || page.Items[1].ID != solto {
		t.Errorf("List by group A plus ungrouped returned %+v", page.Items)
	}

	page, _ = s.Monitors().List(ctx, store.MonitorFilter{})
	if len(page.Items) != 3 {
		t.Errorf("List without selection returned %d, want 3", len(page.Items))
	}
}

// Seleção vazia é "nenhum grupo", não "sem filtro". Confundir os dois
// faria uma conta com escopo esvaziado enxergar a instalação inteira.
func testMonitorListEmptySelectionReturnsNothing(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	mustCreateMonitorIn(t, s, "solto", nil)

	page, err := s.Monitors().List(ctx, store.MonitorFilter{Groups: &store.GroupSelection{}})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("empty selection returned %d monitors, want 0", len(page.Items))
	}
}

func testIncidentListFiltersByGroups(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateGroup(t, s, "A", nil)
	emA := mustCreateMonitorIn(t, s, "em-a", &a.ID)
	solto := mustCreateMonitorIn(t, s, "solto", nil)
	for _, id := range []int64{emA, solto} {
		i := domain.Incident{MonitorID: id, StartedAt: epoch}
		if err := s.Incidents().Open(ctx, &i); err != nil {
			t.Fatalf("Open returned unexpected error: %v", err)
		}
	}

	page, err := s.Incidents().List(ctx, store.IncidentFilter{Groups: &store.GroupSelection{IDs: []int64{a.ID}}})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].MonitorID != emA {
		t.Errorf("incidents by group returned %+v, want only em-a", page.Items)
	}

	page, _ = s.Incidents().List(ctx, store.IncidentFilter{Groups: &store.GroupSelection{}})
	if len(page.Items) != 0 {
		t.Errorf("empty selection returned %d incidents, want 0", len(page.Items))
	}
}

// ---------- escopo de conta ----------

// Toda conta anterior aos grupos alcança tudo. Uma conta criada sem dizer
// escopo também: restringir é sempre explícito.
func testUserScopeDefaultsToAllGroups(t *testing.T, newStore Factory) {
	s := newStore(t)
	u := mustCreateUser(t, s, "ana")

	got, err := s.Users().Get(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Scope.Restricted {
		t.Errorf("Scope = %+v, want all groups", got.Scope)
	}
}

// O escopo precisa sobreviver à ida e volta por todos os caminhos de
// leitura: uma leitura que o perdesse devolveria o valor zero, que é
// "todos os grupos" — o modo de falhar seria conceder.
func testUserScopeRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateGroup(t, s, "A", nil)
	b := mustCreateGroup(t, s, "B", nil)

	u := domain.User{Username: "ana", Role: domain.RoleViewer,
		Scope: domain.Scope{Restricted: true, GroupIDs: []int64{b.ID, a.ID}}}
	if err := u.SetPassword("uma-senha-bem-longa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users().Create(ctx, &u); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	for nome, ler := range map[string]func() (domain.User, error){
		"Get":           func() (domain.User, error) { return s.Users().Get(ctx, u.ID) },
		"GetByUsername": func() (domain.User, error) { return s.Users().GetByUsername(ctx, "ana") },
	} {
		got, err := ler()
		if err != nil {
			t.Fatalf("%s returned unexpected error: %v", nome, err)
		}
		if !got.Scope.Restricted || len(got.Scope.GroupIDs) != 2 ||
			got.Scope.GroupIDs[0] != a.ID || got.Scope.GroupIDs[1] != b.ID {
			t.Errorf("%s: Scope = %+v, want restricted to [%d %d]", nome, got.Scope, a.ID, b.ID)
		}
	}

	// SetAccess substitui a lista, não acumula.
	u.Scope = domain.Scope{Restricted: true, GroupIDs: []int64{a.ID}}
	if err := s.Users().SetAccess(ctx, u.ID, u.Role, &u.Scope); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}
	got, _ := s.Users().Get(ctx, u.ID)
	if len(got.Scope.GroupIDs) != 1 || got.Scope.GroupIDs[0] != a.ID {
		t.Errorf("Scope after SetAccess = %+v, want [%d]", got.Scope, a.ID)
	}

	todos := domain.Scope{}
	if err := s.Users().SetAccess(ctx, u.ID, u.Role, &todos); err != nil {
		t.Fatalf("SetAccess returned unexpected error: %v", err)
	}
	got, _ = s.Users().Get(ctx, u.ID)
	if got.Scope.Restricted || len(got.Scope.GroupIDs) != 0 {
		t.Errorf("Scope after widening = %+v, want all groups", got.Scope)
	}
}

func testUserScopeSurvivesList(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateGroup(t, s, "A", nil)
	mustCreateUser(t, s, "admin")
	u := domain.User{Username: "ana", Role: domain.RoleViewer,
		Scope: domain.Scope{Restricted: true, GroupIDs: []int64{a.ID}}}
	_ = u.SetPassword("uma-senha-bem-longa")
	if err := s.Users().Create(ctx, &u); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	contas, err := s.Users().List(ctx)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(contas) != 2 || contas[0].Scope.Restricted || !contas[1].Scope.Restricted ||
		len(contas[1].Scope.GroupIDs) != 1 {
		t.Errorf("List lost the scope: %+v", contas)
	}
}

// Apagar um grupo tira a concessão, e a conta fica com menos — nunca com
// mais. Um escopo esvaziado continua restrito.
func testUserScopeLosesDeletedGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateGroup(t, s, "A", nil)
	u := domain.User{Username: "ana", Role: domain.RoleViewer,
		Scope: domain.Scope{Restricted: true, GroupIDs: []int64{a.ID}}}
	_ = u.SetPassword("uma-senha-bem-longa")
	if err := s.Users().Create(ctx, &u); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	if err := s.Groups().Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	got, _ := s.Users().Get(ctx, u.ID)
	if !got.Scope.Restricted || len(got.Scope.GroupIDs) != 0 {
		t.Errorf("Scope after deleting the group = %+v, want restricted and empty", got.Scope)
	}
}

// ---------- aviso por grupo ----------

func testChannelGroupLinkRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	canal := mustCreateChannel(t, s, "discord")

	for i := 0; i < 2; i++ {
		if err := s.Channels().LinkGroup(ctx, g.ID, canal.ID); err != nil {
			t.Fatalf("LinkGroup returned unexpected error on attempt %d: %v", i+1, err)
		}
	}
	canais, err := s.Channels().ForGroup(ctx, g.ID)
	if err != nil {
		t.Fatalf("ForGroup returned unexpected error: %v", err)
	}
	if len(canais) != 1 || canais[0].ID != canal.ID {
		t.Errorf("ForGroup returned %+v, want the linked channel once", canais)
	}

	if err := s.Channels().UnlinkGroup(ctx, g.ID, canal.ID); err != nil {
		t.Fatalf("UnlinkGroup returned unexpected error: %v", err)
	}
	if canais, _ := s.Channels().ForGroup(ctx, g.ID); len(canais) != 0 {
		t.Errorf("ForGroup after UnlinkGroup returned %d channels, want 0", len(canais))
	}
}

// Vincular a um canal, monitor ou grupo que não existe é ErrNotFound. A
// API confere a existência antes, mas o canal pode sumir entre a
// conferência e o vínculo; a chave estrangeira recusa, e a recusa precisa
// chegar como "não existe", não como erro interno.
func testChannelLinkToMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	m := mustCreateMonitorIn(t, s, "api", nil)
	canal := mustCreateChannel(t, s, "discord")

	for nome, err := range map[string]error{
		"canal inexistente no monitor": s.Channels().Link(ctx, m, 9999),
		"monitor inexistente":          s.Channels().Link(ctx, 9999, canal.ID),
		"canal inexistente no grupo":   s.Channels().LinkGroup(ctx, g.ID, 9999),
		"grupo inexistente":            s.Channels().LinkGroup(ctx, 9999, canal.ID),
	} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", nome, err)
		}
	}
}

// ListFor devolve os canais que avisam sobre algo da seleção: vinculados
// a um monitor dos grupos escolhidos, ou ao próprio grupo. É a listagem
// de canais de quem tem escopo — o canal que só avisa sobre os outros diz,
// pelo nome e pelo destino, quem existe fora.
func testChannelListForSelectsByLinks(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	prod := mustCreateGroup(t, s, "prod", &rpa.ID)
	ret := mustCreateGroup(t, s, "Retaguarda", nil)
	noProd := mustCreateMonitorIn(t, s, "bff", &prod.ID)
	naRet := mustCreateMonitorIn(t, s, "erp", &ret.ID)
	solto := mustCreateMonitorIn(t, s, "solto", nil)

	doMonitor := mustCreateChannel(t, s, "do-monitor")
	doSistema := mustCreateChannel(t, s, "do-sistema")
	daRet := mustCreateChannel(t, s, "da-ret")
	doSolto := mustCreateChannel(t, s, "do-solto")
	mustCreateChannel(t, s, "de-ninguem")
	// Desligado continua listado: quem administra precisa vê-lo para
	// religar.
	desligado := mustCreateChannel(t, s, "desligado")
	desligado.Enabled = false
	if err := s.Channels().Update(ctx, desligado); err != nil {
		t.Fatal(err)
	}

	for _, l := range []struct{ monitor, canal int64 }{
		{noProd, doMonitor.ID}, {noProd, desligado.ID}, {naRet, daRet.ID}, {solto, doSolto.ID},
	} {
		if err := s.Channels().Link(ctx, l.monitor, l.canal); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Channels().LinkGroup(ctx, rpa.ID, doSistema.ID); err != nil {
		t.Fatal(err)
	}

	nomes := func(sel store.GroupSelection) []string {
		t.Helper()
		canais, err := s.Channels().ListFor(ctx, sel)
		if err != nil {
			t.Fatalf("ListFor(%+v) returned unexpected error: %v", sel, err)
		}
		out := []string{}
		for _, c := range canais {
			out = append(out, c.Name)
		}
		return out
	}

	casos := []struct {
		nome string
		sel  store.GroupSelection
		quer []string
	}{
		{"só o ambiente", store.GroupSelection{IDs: []int64{prod.ID}}, []string{"do-monitor", "desligado"}},
		{"sistema e ambiente", store.GroupSelection{IDs: []int64{rpa.ID, prod.ID}}, []string{"do-monitor", "do-sistema", "desligado"}},
		{"sem grupo", store.GroupSelection{Ungrouped: true}, []string{"do-solto"}},
		{"seleção vazia", store.GroupSelection{}, []string{}},
	}
	for _, c := range casos {
		if got := nomes(c.sel); !sameStrings(got, c.quer) {
			t.Errorf("%s: ListFor = %v, want %v", c.nome, got, c.quer)
		}
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Canal do grupo e canal do sistema valem para o monitor, junto do canal
// vinculado direto nele — e cada destino recebe uma vez só, mesmo
// vinculado pelos três caminhos.
func testChannelDestinationsIncludeGroupAndParent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	prod := mustCreateGroup(t, s, "prod", &rpa.ID)
	outro := mustCreateGroup(t, s, "Outro", nil)
	bff := mustCreateMonitorIn(t, s, "bff", &prod.ID)

	direto := mustCreateChannel(t, s, "direto")
	doAmbiente := mustCreateChannel(t, s, "ambiente")
	doSistema := mustCreateChannel(t, s, "sistema")
	alheio := mustCreateChannel(t, s, "alheio")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Channels().Link(ctx, bff, direto.ID))
	must(s.Channels().LinkGroup(ctx, prod.ID, doAmbiente.ID))
	must(s.Channels().LinkGroup(ctx, rpa.ID, doSistema.ID))
	must(s.Channels().LinkGroup(ctx, outro.ID, alheio.ID))
	// O mesmo canal pelos três caminhos.
	must(s.Channels().LinkGroup(ctx, prod.ID, direto.ID))
	must(s.Channels().LinkGroup(ctx, rpa.ID, direto.ID))

	got, err := s.Channels().Destinations(ctx, bff)
	if err != nil {
		t.Fatalf("Destinations returned unexpected error: %v", err)
	}
	ids := map[int64]int{}
	for _, c := range got {
		ids[c.ID]++
	}
	if len(got) != 3 || ids[direto.ID] != 1 || ids[doAmbiente.ID] != 1 || ids[doSistema.ID] != 1 {
		t.Errorf("Destinations = %+v, want direto, ambiente e sistema uma vez cada", got)
	}
}

// O vínculo é com o grupo, não com a lista de monitores do dia: um
// monitor criado depois já nasce avisando.
func testChannelDestinationsCoverFutureMonitors(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	canal := mustCreateChannel(t, s, "discord")
	if err := s.Channels().LinkGroup(ctx, g.ID, canal.ID); err != nil {
		t.Fatal(err)
	}

	novo := mustCreateMonitorIn(t, s, "novo", &g.ID)
	got, _ := s.Channels().Destinations(ctx, novo)
	if len(got) != 1 {
		t.Errorf("Destinations for a monitor created later returned %d channels, want 1", len(got))
	}
}

func testChannelDestinationsSkipDisabled(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	id := mustCreateMonitorIn(t, s, "bff", &g.ID)
	canal := mustCreateChannel(t, s, "discord")
	if err := s.Channels().LinkGroup(ctx, g.ID, canal.ID); err != nil {
		t.Fatal(err)
	}
	canal.Enabled = false
	if err := s.Channels().Update(ctx, canal); err != nil {
		t.Fatal(err)
	}

	if got, _ := s.Channels().Destinations(ctx, id); len(got) != 0 {
		t.Errorf("Destinations returned %d channels, want the disabled one skipped", len(got))
	}
	if got, _ := s.Channels().ForGroup(ctx, g.ID); len(got) != 0 {
		t.Errorf("ForGroup returned %d channels, want the disabled one skipped", len(got))
	}
}

func testChannelDestinationsWithoutGroup(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	id := mustCreateMonitorIn(t, s, "solto", nil)
	canal := mustCreateChannel(t, s, "discord")
	if err := s.Channels().Link(ctx, id, canal.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.Channels().Destinations(ctx, id)
	if err != nil {
		t.Fatalf("Destinations returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("Destinations for an ungrouped monitor returned %d channels, want 1", len(got))
	}
}

// ---------- histórico de mudanças de estado ----------

func testTransitionRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	tr := domain.Transition{
		MonitorID: id, From: domain.StatusUp, To: domain.StatusDown,
		At: epoch, Cause: "connection refused",
	}
	if err := s.Transitions().Record(ctx, &tr); err != nil {
		t.Fatalf("Record returned unexpected error: %v", err)
	}
	if tr.ID == 0 {
		t.Error("Record left ID as zero")
	}

	got, err := s.Transitions().List(ctx, store.TransitionFilter{MonitorID: id})
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d transitions, want 1", len(got))
	}
	if got[0].From != domain.StatusUp || got[0].To != domain.StatusDown ||
		!got[0].At.Equal(epoch) || got[0].Cause != "connection refused" {
		t.Errorf("transition = %+v", got[0])
	}
}

// A linha do tempo se lê do mais recente para trás, e a página seguinte
// começa onde a anterior parou.
func testTransitionListIsNewestFirstAndPaginates(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")
	outro := mustCreateMonitor(t, s, "outro")

	for i := 0; i < 5; i++ {
		tr := domain.Transition{MonitorID: id, From: domain.StatusUp, To: domain.StatusDown,
			At: epoch.Add(time.Duration(i) * time.Minute)}
		if err := s.Transitions().Record(ctx, &tr); err != nil {
			t.Fatal(err)
		}
	}
	alheia := domain.Transition{MonitorID: outro, From: domain.StatusUp, To: domain.StatusDown, At: epoch}
	_ = s.Transitions().Record(ctx, &alheia)

	primeira, _ := s.Transitions().List(ctx, store.TransitionFilter{MonitorID: id, Limit: 2})
	if len(primeira) != 2 || !primeira[0].At.Equal(epoch.Add(4*time.Minute)) {
		t.Fatalf("first page = %+v, want the two newest", primeira)
	}

	segunda, _ := s.Transitions().List(ctx, store.TransitionFilter{
		MonitorID: id, Limit: 10, Before: primeira[1].At,
	})
	if len(segunda) != 3 || !segunda[0].At.Equal(epoch.Add(2*time.Minute)) {
		t.Errorf("second page = %+v, want the three older ones", segunda)
	}
}

func testTransitionCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	tr := domain.Transition{MonitorID: id, From: domain.StatusUp, To: domain.StatusDown, At: epoch}
	_ = s.Transitions().Record(ctx, &tr)
	if err := s.Monitors().Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Transitions().List(ctx, store.TransitionFilter{MonitorID: id}); len(got) != 0 {
		t.Errorf("found %d transitions after deleting the monitor, want 0", len(got))
	}
}

// A aba de eventos lista as batidas com falha. Filtrar no banco, e não
// depois do limite, é o que impede uma janela de batidas boas de esconder
// a falha que veio antes delas.
func testHeartbeatQueryOnlyFailures(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	id := mustCreateMonitor(t, s, "api")

	hbs := []domain.Heartbeat{
		beat(id, 0, domain.StatusDown, 0),
		beat(id, time.Minute, domain.StatusDegraded, 900),
	}
	for i := 0; i < 10; i++ {
		hbs = append(hbs, beat(id, time.Duration(2+i)*time.Minute, domain.StatusUp, 10))
	}
	if err := s.WriteHeartbeats(ctx, hbs); err != nil {
		t.Fatal(err)
	}

	got, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: id, Range: fullRange(), Limit: 5, OnlyFailures: true,
	})
	if err != nil {
		t.Fatalf("QueryHeartbeats returned unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Status != domain.StatusDown || got[1].Status != domain.StatusDegraded {
		t.Errorf("failures = %+v, want down and degraded", got)
	}
}
