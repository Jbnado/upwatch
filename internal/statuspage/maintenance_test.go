package statuspage_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

func declararManutencao(t *testing.T, s store.Store, w domain.MaintenanceWindow) {
	t.Helper()
	if w.Title == "" {
		w.Title = "Atualização do banco"
	}
	if w.TimeZone == "" {
		w.TimeZone = "UTC"
	}
	if w.Recurrence == "" {
		w.Recurrence = domain.RecurrenceNone
	}
	if err := s.Maintenance().Create(context.Background(), &w); err != nil {
		t.Fatalf("declarando manutenção: %v", err)
	}
}

// Durante a janela a página diz "manutenção", e não queda: quem abre o
// link no meio do deploy precisa saber que era esperado.
func TestBuildMostraManutencaoEmCurso(t *testing.T) {
	s := newStore(t)
	p := criarPagina(t, s, "estado")
	api := criarMonitor(t, s, "api", "https://api.interna")
	outro := criarMonitor(t, s, "site", "https://site.interno")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: api.ID, Label: "API"})
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: outro.ID, Label: "Site", Position: 1})

	declararManutencao(t, s, domain.MaintenanceWindow{
		Message:  "A API fica indisponível por até meia hora.",
		StartsAt: agora.Add(-10 * time.Minute), EndsAt: agora.Add(20 * time.Minute),
		MonitorIDs: []int64{api.ID},
	})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(view.Maintenances) != 1 {
		t.Fatalf("Maintenances = %+v, want one", view.Maintenances)
	}
	m := view.Maintenances[0]
	if !m.Active || m.Message == "" || !slices.Equal(m.Components, []string{"API"}) {
		t.Errorf("maintenance = %+v, want active, with the message and only the API", m)
	}

	porNome := map[string]domain.PublicMonitor{}
	for _, g := range view.Groups {
		for _, pm := range g.Monitors {
			porNome[pm.Name] = pm
		}
	}
	if !porNome["API"].Maintenance || porNome["Site"].Maintenance {
		t.Errorf("maintenance flags = API:%v Site:%v, want only the API", porNome["API"].Maintenance, porNome["Site"].Maintenance)
	}
}

// A página anuncia antes: a recorrente de domingo aparece na semana dela,
// e não a de daqui a um mês.
func TestBuildAnunciaManutencaoProxima(t *testing.T) {
	s := newStore(t)
	p := criarPagina(t, s, "estado")
	api := criarMonitor(t, s, "api", "https://api.interna")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: api.ID, Label: "API"})

	declararManutencao(t, s, domain.MaintenanceWindow{
		Title:    "Janela semanal",
		StartsAt: agora.Add(-6 * 24 * time.Hour), EndsAt: agora.Add(-6*24*time.Hour + time.Hour),
		Recurrence: domain.RecurrenceWeekly, MonitorIDs: []int64{api.ID},
	})
	declararManutencao(t, s, domain.MaintenanceWindow{
		Title:    "Migração do mês que vem",
		StartsAt: agora.Add(30 * 24 * time.Hour), EndsAt: agora.Add(30*24*time.Hour + time.Hour),
		MonitorIDs: []int64{api.ID},
	})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(view.Maintenances) != 1 || view.Maintenances[0].Title != "Janela semanal" {
		t.Fatalf("Maintenances = %+v, want only the weekly window", view.Maintenances)
	}
	m := view.Maintenances[0]
	if m.Active || !m.StartsAt.Equal(agora.Add(24*time.Hour)) {
		t.Errorf("maintenance = %+v, want the next occurrence tomorrow, not active", m)
	}
}

// Vazamento: a janela que também cobre um alvo interno não pode contar a
// esta página que ele existe, e a janela que só cobre o que esta página
// não publica não aparece.
func TestBuildManutencaoNaoRevelaAlvoNaoPublicado(t *testing.T) {
	s := newStore(t)
	p := criarPagina(t, s, "estado")
	api := criarMonitor(t, s, "api", "https://api.interna")
	banco := criarMonitor(t, s, "banco-secreto", "postgres://banco-interno.vpc.local:5432")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: api.ID, Label: "API"})

	declararManutencao(t, s, domain.MaintenanceWindow{
		Title:    "Troca de servidor",
		StartsAt: agora.Add(-time.Minute), EndsAt: agora.Add(time.Hour),
		MonitorIDs: []int64{api.ID, banco.ID},
	})
	declararManutencao(t, s, domain.MaintenanceWindow{
		Title:    "Só o banco-secreto",
		StartsAt: agora.Add(-time.Minute), EndsAt: agora.Add(time.Hour),
		MonitorIDs: []int64{banco.ID},
	})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, segredo := range []string{"banco-secreto", "banco-interno", "Só o banco"} {
		if strings.Contains(string(raw), segredo) {
			t.Errorf("public view leaks %q: %s", segredo, raw)
		}
	}
	if len(view.Maintenances) != 1 || !slices.Equal(view.Maintenances[0].Components, []string{"API"}) {
		t.Errorf("Maintenances = %+v, want only the shared window, naming only the API", view.Maintenances)
	}
}

// A regra é a do motor: a janela sobre o grupo vale para o ambiente, e a
// janela sobre o pai vale para o dependente publicado.
func TestBuildManutencaoSegueGrupoEDependencia(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	p := criarPagina(t, s, "estado")

	rpa := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := s.Groups().Create(ctx, &rpa); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}
	prod := domain.Group{Name: "prod", Slug: "prod", ParentID: &rpa.ID}
	if err := s.Groups().Create(ctx, &prod); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}
	api := criarMonitor(t, s, "api", "https://api.interna")
	api.GroupID = &prod.ID
	if err := s.Monitors().Update(ctx, api); err != nil {
		t.Fatalf("movendo monitor: %v", err)
	}
	sonda := criarMonitor(t, s, "sonda", "https://sonda.interna")
	filho := criarMonitor(t, s, "redis", "redis://redis.interno")
	filho.ParentID = &sonda.ID
	if err := s.Monitors().Update(ctx, filho); err != nil {
		t.Fatalf("definindo pai: %v", err)
	}
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: api.ID, Label: "API"})
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: filho.ID, Label: "Cache", Position: 1})

	declararManutencao(t, s, domain.MaintenanceWindow{
		StartsAt: agora.Add(-time.Minute), EndsAt: agora.Add(time.Hour),
		GroupIDs: []int64{rpa.ID}, MonitorIDs: []int64{sonda.ID},
	})

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(view.Maintenances) != 1 || !slices.Equal(view.Maintenances[0].Components, []string{"API", "Cache"}) {
		t.Errorf("Maintenances = %+v, want API (via RPA) and Cache (via its parent)", view.Maintenances)
	}
}
