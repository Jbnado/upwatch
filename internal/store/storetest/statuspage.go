package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// Conformidade das páginas públicas e dos relatos.
//
// Boa parte do que se verifica aqui é cascata e alcance. A página é a
// única superfície sem credencial, e os erros que importam não são de
// escrita — são de visibilidade: um componente que sobrevive à exclusão
// do monitor, um relato que passa a aparecer onde não devia, um slug
// duplicado que faz duas páginas responderem no mesmo endereço.

func statusPageCases() []conformanceCase {
	return []conformanceCase{
		{"StatusPageCreateAssignsID", testStatusPageCreateAssignsID},
		{"StatusPageRoundTripsAllFields", testStatusPageRoundTripsAllFields},
		{"StatusPageDuplicateSlugReturnsConflict", testStatusPageDuplicateSlugReturnsConflict},
		{"StatusPageGetBySlugFindsIt", testStatusPageGetBySlugFindsIt},
		{"StatusPageGetBySlugMissingReturnsNotFound", testStatusPageGetBySlugMissingReturnsNotFound},
		{"StatusPageUpdatePersistsChanges", testStatusPageUpdatePersistsChanges},
		{"StatusPageDeleteRemovesIt", testStatusPageDeleteRemovesIt},
		{"StatusPageListIsOrdered", testStatusPageListIsOrdered},

		{"StatusPageGroupRoundTrips", testStatusPageGroupRoundTrips},
		{"StatusPageGroupsAreOrderedByPosition", testStatusPageGroupsAreOrderedByPosition},
		{"StatusPageGroupCascadesOnPageDelete", testStatusPageGroupCascadesOnPageDelete},

		{"StatusPageComponentRoundTrips", testStatusPageComponentRoundTrips},
		{"StatusPageComponentSetIsIdempotent", testStatusPageComponentSetIsIdempotent},
		{"StatusPageComponentsAreOrderedByPosition", testStatusPageComponentsAreOrderedByPosition},
		{"StatusPageComponentCascadesOnMonitorDelete", testStatusPageComponentCascadesOnMonitorDelete},
		{"StatusPageComponentCascadesOnPageDelete", testStatusPageComponentCascadesOnPageDelete},
		{"StatusPageComponentSurvivesGroupDelete", testStatusPageComponentSurvivesGroupDelete},
		{"StatusPageComponentRemoveIsScopedToPage", testStatusPageComponentRemoveIsScopedToPage},

		{"AnnouncementCreateAssignsID", testAnnouncementCreateAssignsID},
		{"AnnouncementRoundTripsComponents", testAnnouncementRoundTripsComponents},
		{"AnnouncementUpdateReplacesComponents", testAnnouncementUpdateReplacesComponents},
		{"AnnouncementComponentCascadesOnMonitorDelete", testAnnouncementComponentCascadesOnMonitorDelete},
		{"AnnouncementSurvivesIncidentDelete", testAnnouncementSurvivesIncidentDelete},
		{"AnnouncementUpdatesAreChronological", testAnnouncementUpdatesAreChronological},
		{"AnnouncementUpdatesCascadeOnDelete", testAnnouncementUpdatesCascadeOnDelete},
		{"AnnouncementListFiltersBySince", testAnnouncementListFiltersBySince},
		{"AnnouncementListFiltersByOpen", testAnnouncementListFiltersByOpen},
		{"AnnouncementListIsNewestFirst", testAnnouncementListIsNewestFirst},
	}
}

// ---------- helpers ----------

func newStatusPage(slug string) domain.StatusPage {
	return domain.StatusPage{
		Slug:    slug,
		Title:   "Estado da plataforma",
		Enabled: true,
	}
}

func newAnnouncement(title string) domain.Announcement {
	return domain.Announcement{
		Title:     title,
		Impact:    domain.ImpactMajor,
		Phase:     domain.PhaseInvestigating,
		Global:    true,
		StartedAt: epoch,
	}
}

func createStatusPage(t *testing.T, s store.Store, slug string) domain.StatusPage {
	t.Helper()

	p := newStatusPage(slug)
	if err := s.StatusPages().Create(context.Background(), &p); err != nil {
		t.Fatalf("criando página %q: %v", slug, err)
	}
	return p
}

func createAnnouncement(t *testing.T, s store.Store, a domain.Announcement) domain.Announcement {
	t.Helper()

	if err := s.Announcements().Create(context.Background(), &a); err != nil {
		t.Fatalf("criando relato %q: %v", a.Title, err)
	}
	return a
}

// ---------- páginas ----------

func testStatusPageCreateAssignsID(t *testing.T, newStore Factory) {
	s := newStore(t)

	p := createStatusPage(t, s, "estado")

	if p.ID == 0 {
		t.Fatal("página criada sem ID")
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Fatal("página criada sem carimbo de tempo")
	}
}

func testStatusPageRoundTripsAllFields(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := domain.StatusPage{
		Slug:        "estado-br",
		Title:       "Estado — Brasil",
		Description: "Disponibilidade dos serviços da região.",
		ShowLatency: true,
		TimeZone:    "America/Sao_Paulo",
		Enabled:     false,
	}
	if err := s.StatusPages().Create(ctx, &p); err != nil {
		t.Fatalf("criando: %v", err)
	}

	volta, err := s.StatusPages().Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}

	if volta.Slug != p.Slug || volta.Title != p.Title || volta.Description != p.Description {
		t.Errorf("texto divergiu: %+v", volta)
	}
	if !volta.ShowLatency {
		t.Error("show_latency não sobreviveu")
	}
	if volta.TimeZone != "America/Sao_Paulo" {
		t.Errorf("fuso divergiu: %q", volta.TimeZone)
	}
	// Enabled false precisa sobreviver: é o que decide se a página
	// responde ou devolve 404, e um default aplicado na leitura publicaria
	// uma página que alguém desligou de propósito.
	if volta.Enabled {
		t.Error("página desligada voltou ligada")
	}
}

func testStatusPageDuplicateSlugReturnsConflict(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	createStatusPage(t, s, "estado")

	outra := newStatusPage("estado")
	err := s.StatusPages().Create(ctx, &outra)

	// Dois slugs iguais fariam duas páginas responderem no mesmo endereço,
	// e qual delas responde dependeria da ordem da varredura.
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("esperava ErrConflict, veio %v", err)
	}
}

func testStatusPageGetBySlugFindsIt(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	criada := createStatusPage(t, s, "estado")

	volta, err := s.StatusPages().GetBySlug(ctx, "estado")
	if err != nil {
		t.Fatalf("buscando por slug: %v", err)
	}
	if volta.ID != criada.ID {
		t.Fatalf("trouxe a página errada: %d != %d", volta.ID, criada.ID)
	}
}

func testStatusPageGetBySlugMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)

	_, err := s.StatusPages().GetBySlug(context.Background(), "nao-existe")

	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

func testStatusPageUpdatePersistsChanges(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	p.Title = "Outro título"
	p.Enabled = false
	if err := s.StatusPages().Update(ctx, p); err != nil {
		t.Fatalf("atualizando: %v", err)
	}

	volta, err := s.StatusPages().Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if volta.Title != "Outro título" || volta.Enabled {
		t.Fatalf("alteração não persistiu: %+v", volta)
	}
}

func testStatusPageDeleteRemovesIt(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	if err := s.StatusPages().Delete(ctx, p.ID); err != nil {
		t.Fatalf("apagando: %v", err)
	}

	if _, err := s.StatusPages().Get(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

func testStatusPageListIsOrdered(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	for _, slug := range []string{"c", "a", "b"} {
		createStatusPage(t, s, slug)
	}

	paginas, err := s.StatusPages().List(ctx)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(paginas) != 3 {
		t.Fatalf("esperava 3 páginas, vieram %d", len(paginas))
	}
	for i := 1; i < len(paginas); i++ {
		if paginas[i].ID < paginas[i-1].ID {
			t.Fatal("listagem fora de ordem")
		}
	}
}

// ---------- grupos ----------

func testStatusPageGroupRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	g := domain.StatusPageGroup{PageID: p.ID, Name: "API", Position: 2}
	if err := s.StatusPages().CreateGroup(ctx, &g); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}
	if g.ID == 0 {
		t.Fatal("grupo criado sem ID")
	}

	grupos, err := s.StatusPages().Groups(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando grupos: %v", err)
	}
	if len(grupos) != 1 || grupos[0].Name != "API" || grupos[0].Position != 2 {
		t.Fatalf("grupo divergiu: %+v", grupos)
	}
}

func testStatusPageGroupsAreOrderedByPosition(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	// Inseridos fora de ordem de propósito: a ordem da página é editorial,
	// definida por quem publica, e não pode depender da ordem de inserção.
	for _, g := range []domain.StatusPageGroup{
		{PageID: p.ID, Name: "Webhooks", Position: 3},
		{PageID: p.ID, Name: "API", Position: 1},
		{PageID: p.ID, Name: "Console", Position: 2},
	} {
		grupo := g
		if err := s.StatusPages().CreateGroup(ctx, &grupo); err != nil {
			t.Fatalf("criando grupo: %v", err)
		}
	}

	grupos, err := s.StatusPages().Groups(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}

	nomes := []string{grupos[0].Name, grupos[1].Name, grupos[2].Name}
	esperado := []string{"API", "Console", "Webhooks"}
	for i := range esperado {
		if nomes[i] != esperado[i] {
			t.Fatalf("ordem divergiu: %v", nomes)
		}
	}
}

func testStatusPageGroupCascadesOnPageDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	g := domain.StatusPageGroup{PageID: p.ID, Name: "API"}
	if err := s.StatusPages().CreateGroup(ctx, &g); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}

	if err := s.StatusPages().Delete(ctx, p.ID); err != nil {
		t.Fatalf("apagando página: %v", err)
	}

	grupos, err := s.StatusPages().Groups(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(grupos) != 0 {
		t.Fatalf("grupo sobreviveu à exclusão da página: %+v", grupos)
	}
}

// ---------- componentes ----------

func testStatusPageComponentRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	m := mustCreateMonitor(t, s, "api-prod-us-east-1")
	g := domain.StatusPageGroup{PageID: p.ID, Name: "API"}
	if err := s.StatusPages().CreateGroup(ctx, &g); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}

	c := domain.StatusPageComponent{
		PageID: p.ID, MonitorID: m, GroupID: &g.ID, Label: "API", Position: 1,
	}
	if err := s.StatusPages().SetComponent(ctx, c); err != nil {
		t.Fatalf("vinculando componente: %v", err)
	}

	comps, err := s.StatusPages().Components(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando componentes: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("esperava 1 componente, vieram %d", len(comps))
	}
	// O rótulo é o que separa o nome interno do público: sem ele, publicar
	// a página entregaria a convenção de nomes da infraestrutura.
	if comps[0].Label != "API" {
		t.Errorf("rótulo divergiu: %q", comps[0].Label)
	}
	if comps[0].GroupID == nil || *comps[0].GroupID != g.ID {
		t.Errorf("grupo divergiu: %+v", comps[0].GroupID)
	}
}

func testStatusPageComponentSetIsIdempotent(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	m := mustCreateMonitor(t, s, "api")

	// A interface reenvia o conjunto inteiro em vez de calcular a
	// diferença; vincular duas vezes precisa atualizar, não duplicar.
	primeiro := domain.StatusPageComponent{PageID: p.ID, MonitorID: m, Label: "API"}
	segundo := domain.StatusPageComponent{PageID: p.ID, MonitorID: m, Label: "API pública", Position: 5}

	if err := s.StatusPages().SetComponent(ctx, primeiro); err != nil {
		t.Fatalf("primeiro vínculo: %v", err)
	}
	if err := s.StatusPages().SetComponent(ctx, segundo); err != nil {
		t.Fatalf("segundo vínculo: %v", err)
	}

	comps, err := s.StatusPages().Components(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("vínculo duplicou: %d componentes", len(comps))
	}
	if comps[0].Label != "API pública" || comps[0].Position != 5 {
		t.Fatalf("segundo vínculo não atualizou: %+v", comps[0])
	}
}

func testStatusPageComponentsAreOrderedByPosition(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	primeiro := mustCreateMonitor(t, s, "z-alvo")
	segundo := mustCreateMonitor(t, s, "a-alvo")

	if err := s.StatusPages().SetComponent(ctx,
		domain.StatusPageComponent{PageID: p.ID, MonitorID: primeiro, Position: 1}); err != nil {
		t.Fatalf("vinculando: %v", err)
	}
	if err := s.StatusPages().SetComponent(ctx,
		domain.StatusPageComponent{PageID: p.ID, MonitorID: segundo, Position: 2}); err != nil {
		t.Fatalf("vinculando: %v", err)
	}

	comps, err := s.StatusPages().Components(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if comps[0].MonitorID != primeiro {
		t.Fatal("ordem seguiu o nome do monitor em vez da posição escolhida")
	}
}

func testStatusPageComponentCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	m := mustCreateMonitor(t, s, "api")
	if err := s.StatusPages().SetComponent(ctx,
		domain.StatusPageComponent{PageID: p.ID, MonitorID: m}); err != nil {
		t.Fatalf("vinculando: %v", err)
	}

	if err := s.Monitors().Delete(ctx, m); err != nil {
		t.Fatalf("apagando monitor: %v", err)
	}

	comps, err := s.StatusPages().Components(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	// Componente órfão apontaria para um monitor inexistente, e a página
	// pública tentaria montar uma linha sem histórico nem estado.
	if len(comps) != 0 {
		t.Fatalf("componente sobreviveu à exclusão do monitor: %+v", comps)
	}
}

func testStatusPageComponentCascadesOnPageDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	m := mustCreateMonitor(t, s, "api")
	if err := s.StatusPages().SetComponent(ctx,
		domain.StatusPageComponent{PageID: p.ID, MonitorID: m}); err != nil {
		t.Fatalf("vinculando: %v", err)
	}

	if err := s.StatusPages().Delete(ctx, p.ID); err != nil {
		t.Fatalf("apagando página: %v", err)
	}

	// O monitor não pode ir junto: ele é da operação, e a página é só uma
	// das formas de mostrá-lo.
	if _, err := s.Monitors().Get(ctx, m); err != nil {
		t.Fatalf("monitor foi apagado junto com a página: %v", err)
	}
}

func testStatusPageComponentSurvivesGroupDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p := createStatusPage(t, s, "estado")
	m := mustCreateMonitor(t, s, "api")
	g := domain.StatusPageGroup{PageID: p.ID, Name: "API"}
	if err := s.StatusPages().CreateGroup(ctx, &g); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}
	if err := s.StatusPages().SetComponent(ctx,
		domain.StatusPageComponent{PageID: p.ID, MonitorID: m, GroupID: &g.ID}); err != nil {
		t.Fatalf("vinculando: %v", err)
	}

	if err := s.StatusPages().DeleteGroup(ctx, g.ID); err != nil {
		t.Fatalf("apagando grupo: %v", err)
	}

	comps, err := s.StatusPages().Components(ctx, p.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	// Desfazer um agrupamento não pode despublicar o componente: quem
	// arrasta um grupo fora espera reorganizar, não remover da página.
	if len(comps) != 1 {
		t.Fatalf("componente sumiu ao apagar o grupo: %+v", comps)
	}
	if comps[0].GroupID != nil {
		t.Errorf("componente ficou apontando para grupo inexistente: %+v", comps[0].GroupID)
	}
}

func testStatusPageComponentRemoveIsScopedToPage(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	uma := createStatusPage(t, s, "uma")
	outra := createStatusPage(t, s, "outra")
	m := mustCreateMonitor(t, s, "api")

	for _, p := range []domain.StatusPage{uma, outra} {
		if err := s.StatusPages().SetComponent(ctx,
			domain.StatusPageComponent{PageID: p.ID, MonitorID: m}); err != nil {
			t.Fatalf("vinculando: %v", err)
		}
	}

	if err := s.StatusPages().RemoveComponent(ctx, uma.ID, m); err != nil {
		t.Fatalf("removendo: %v", err)
	}

	// Despublicar um alvo da página de um cliente não pode despublicá-lo
	// da página de outro.
	comps, err := s.StatusPages().Components(ctx, outra.ID)
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(comps) != 1 {
		t.Fatalf("remoção vazou para a outra página: %+v", comps)
	}
}

// ---------- relatos ----------

func testAnnouncementCreateAssignsID(t *testing.T, newStore Factory) {
	s := newStore(t)

	a := createAnnouncement(t, s, newAnnouncement("Lentidão na API"))

	if a.ID == 0 {
		t.Fatal("relato criado sem ID")
	}
	if a.CreatedAt.IsZero() {
		t.Fatal("relato criado sem carimbo de tempo")
	}
}

func testAnnouncementRoundTripsComponents(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	um := mustCreateMonitor(t, s, "api")
	outro := mustCreateMonitor(t, s, "console")

	a := newAnnouncement("Falha parcial")
	a.Global = false
	a.Components = []int64{um, outro}
	a = createAnnouncement(t, s, a)

	volta, err := s.Announcements().Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if volta.Global {
		t.Error("relato com componentes voltou marcado como global")
	}
	if len(volta.Components) != 2 {
		t.Fatalf("componentes divergiram: %+v", volta.Components)
	}
	if volta.Impact != domain.ImpactMajor || volta.Phase != domain.PhaseInvestigating {
		t.Errorf("impacto ou fase divergiram: %+v", volta)
	}
}

func testAnnouncementUpdateReplacesComponents(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	um := mustCreateMonitor(t, s, "api")
	outro := mustCreateMonitor(t, s, "console")

	a := newAnnouncement("Falha")
	a.Global = false
	a.Components = []int64{um}
	a = createAnnouncement(t, s, a)

	a.Components = []int64{outro}
	a.Phase = domain.PhaseResolved
	if err := s.Announcements().Update(ctx, a); err != nil {
		t.Fatalf("atualizando: %v", err)
	}

	volta, err := s.Announcements().Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	// Substitui, não acumula: um relato que só afeta o console agora não
	// pode continuar aparecendo na página que publica a API.
	if len(volta.Components) != 1 || volta.Components[0] != outro {
		t.Fatalf("componentes não foram substituídos: %+v", volta.Components)
	}
	if volta.Phase != domain.PhaseResolved {
		t.Errorf("fase não persistiu: %v", volta.Phase)
	}
}

func testAnnouncementComponentCascadesOnMonitorDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	m := mustCreateMonitor(t, s, "api")
	a := newAnnouncement("Falha")
	a.Global = false
	a.Components = []int64{m}
	a = createAnnouncement(t, s, a)

	if err := s.Monitors().Delete(ctx, m); err != nil {
		t.Fatalf("apagando monitor: %v", err)
	}

	volta, err := s.Announcements().Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("relato sumiu junto com o monitor: %v", err)
	}
	// O relato sobrevive — é registro histórico do que foi comunicado —
	// mas fica sem componente. Como não é global, ShowsOn passa a
	// devolver falso: falha fechando, e nenhuma página o exibe por
	// engano.
	if len(volta.Components) != 0 {
		t.Fatalf("componente órfão sobreviveu: %+v", volta.Components)
	}
	if volta.ShowsOn([]int64{1, 2, 3}) {
		t.Error("relato sem componente e sem alcance global apareceu numa página")
	}
}

func testAnnouncementSurvivesIncidentDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	m := mustCreateMonitor(t, s, "api")
	inc := domain.Incident{MonitorID: m, StartedAt: epoch, Cause: "connection refused"}
	if err := s.Incidents().Open(ctx, &inc); err != nil {
		t.Fatalf("abrindo incidente: %v", err)
	}

	a := newAnnouncement("Falha")
	a.IncidentID = &inc.ID
	a = createAnnouncement(t, s, a)

	// Apagar o monitor leva o incidente por cascata.
	if err := s.Monitors().Delete(ctx, m); err != nil {
		t.Fatalf("apagando monitor: %v", err)
	}

	volta, err := s.Announcements().Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("relato sumiu com o incidente: %v", err)
	}
	// O que foi comunicado publicamente não pode desaparecer porque
	// alguém apagou um monitor meses depois: a página é registro.
	if volta.IncidentID != nil {
		t.Errorf("vínculo com incidente inexistente sobreviveu: %v", *volta.IncidentID)
	}
}

func testAnnouncementUpdatesAreChronological(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := createAnnouncement(t, s, newAnnouncement("Falha"))

	// Publicados fora de ordem: a linha do tempo se lê de cima para
	// baixo, e a ordem de inserção não é a ordem dos fatos quando alguém
	// corrige um horário.
	for _, u := range []domain.AnnouncementUpdate{
		{AnnouncementID: a.ID, Phase: domain.PhaseResolved, Body: "Normalizado.", PublishedAt: epoch.Add(2 * time.Hour)},
		{AnnouncementID: a.ID, Phase: domain.PhaseInvestigating, Body: "Investigando.", PublishedAt: epoch},
		{AnnouncementID: a.ID, Phase: domain.PhaseIdentified, Body: "Causa encontrada.", PublishedAt: epoch.Add(time.Hour)},
	} {
		upd := u
		if err := s.Announcements().AddUpdate(ctx, &upd); err != nil {
			t.Fatalf("publicando atualização: %v", err)
		}
	}

	updates, err := s.Announcements().Updates(ctx, a.ID)
	if err != nil {
		t.Fatalf("lendo atualizações: %v", err)
	}
	if len(updates) != 3 {
		t.Fatalf("esperava 3 atualizações, vieram %d", len(updates))
	}
	for i := 1; i < len(updates); i++ {
		if updates[i].PublishedAt.Before(updates[i-1].PublishedAt) {
			t.Fatal("linha do tempo fora de ordem cronológica")
		}
	}
	if updates[0].Phase != domain.PhaseInvestigating {
		t.Errorf("primeira entrada divergiu: %v", updates[0].Phase)
	}
}

func testAnnouncementUpdatesCascadeOnDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := createAnnouncement(t, s, newAnnouncement("Falha"))
	upd := domain.AnnouncementUpdate{
		AnnouncementID: a.ID, Phase: domain.PhaseInvestigating,
		Body: "Investigando.", PublishedAt: epoch,
	}
	if err := s.Announcements().AddUpdate(ctx, &upd); err != nil {
		t.Fatalf("publicando: %v", err)
	}

	if err := s.Announcements().Delete(ctx, a.ID); err != nil {
		t.Fatalf("apagando relato: %v", err)
	}

	updates, err := s.Announcements().Updates(ctx, a.ID)
	if err != nil {
		t.Fatalf("lendo: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("atualizações sobreviveram ao relato: %+v", updates)
	}
}

func testAnnouncementListFiltersBySince(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	antigo := newAnnouncement("Antigo")
	antigo.StartedAt = epoch.Add(-100 * 24 * time.Hour)
	createAnnouncement(t, s, antigo)

	recente := newAnnouncement("Recente")
	recente.StartedAt = epoch
	createAnnouncement(t, s, recente)

	// A página mostra uma janela; sem o corte, uma instalação de dois anos
	// devolveria o histórico inteiro a cada visita anônima.
	page, err := s.Announcements().List(ctx, store.AnnouncementFilter{
		Since: epoch.Add(-90 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Recente" {
		t.Fatalf("filtro por janela divergiu: %+v", page.Items)
	}
}

func testAnnouncementListFiltersByOpen(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	aberto := newAnnouncement("Em curso")
	aberto.Phase = domain.PhaseMonitoring
	createAnnouncement(t, s, aberto)

	fechado := newAnnouncement("Resolvido")
	fechado.Phase = domain.PhaseResolved
	createAnnouncement(t, s, fechado)

	page, err := s.Announcements().List(ctx, store.AnnouncementFilter{OnlyOpen: true})
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Title != "Em curso" {
		t.Fatalf("filtro por abertos divergiu: %+v", page.Items)
	}
}

func testAnnouncementListIsNewestFirst(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	primeiro := newAnnouncement("Mais antigo")
	primeiro.StartedAt = epoch.Add(-2 * time.Hour)
	createAnnouncement(t, s, primeiro)

	segundo := newAnnouncement("Mais recente")
	segundo.StartedAt = epoch
	createAnnouncement(t, s, segundo)

	page, err := s.Announcements().List(ctx, store.AnnouncementFilter{})
	if err != nil {
		t.Fatalf("listando: %v", err)
	}
	// "Incidentes anteriores" se lê do mais recente para o mais antigo: é
	// a queda de ontem que interessa, não a do ano passado.
	if len(page.Items) != 2 || page.Items[0].Title != "Mais recente" {
		t.Fatalf("ordem divergiu: %+v", page.Items)
	}
}
