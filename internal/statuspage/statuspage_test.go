package statuspage_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/statuspage"
	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
)

// A montagem da página pública.
//
// O teste mais importante deste arquivo é o de vazamento. Os demais
// verificam que a página conta a verdade; aquele verifica que ela não
// conta mais do que deveria — e é o único cuja falha entrega informação
// para quem só tem o link.

var agora = time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)

func newStore(t *testing.T) store.Store {
	t.Helper()

	s, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "status.db"))
	if err != nil {
		t.Fatalf("abrindo banco: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newBuilder(s store.Store) *statuspage.Builder {
	return statuspage.NewBuilder(s, clock.NewFake(agora))
}

// criarMonitor cria um alvo com endereço reconhecível, para o teste de
// vazamento conseguir procurá-lo no JSON.
func criarMonitor(t *testing.T, s store.Store, nome, alvo string) domain.Monitor {
	t.Helper()

	m := domain.Monitor{
		Name:                  nome,
		Type:                  domain.MonitorHTTP,
		Target:                alvo,
		Interval:              time.Minute,
		Timeout:               10 * time.Second,
		ConfirmationThreshold: 2,
		Enabled:               true,
	}
	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("criando monitor %q: %v", nome, err)
	}
	return m
}

func criarPagina(t *testing.T, s store.Store, slug string) domain.StatusPage {
	t.Helper()

	p := domain.StatusPage{Slug: slug, Title: "Estado da plataforma", Enabled: true}
	if err := s.StatusPages().Create(context.Background(), &p); err != nil {
		t.Fatalf("criando página: %v", err)
	}
	return p
}

func publicar(t *testing.T, s store.Store, c domain.StatusPageComponent) {
	t.Helper()

	if err := s.StatusPages().SetComponent(context.Background(), c); err != nil {
		t.Fatalf("publicando componente: %v", err)
	}
}

// gravarDia grava um agregado diário com as contagens dadas.
func gravarDia(t *testing.T, s store.Store, monitorID int64, dia time.Time, up, down int) {
	t.Helper()

	r := domain.Rollup{
		MonitorID:   monitorID,
		ProbeID:     "local",
		Resolution:  domain.ResolutionDaily,
		BucketStart: dia.UTC().Truncate(24 * time.Hour),
		Total:       up + down,
		Up:          up,
		Down:        down,
	}
	if err := s.WriteRollups(context.Background(), []domain.Rollup{r}); err != nil {
		t.Fatalf("gravando agregado: %v", err)
	}
}

// ---------- o teste que importa ----------

func TestBuildNaoRevelaNadaInterno(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Cada valor abaixo é uma coisa que jamais pode sair pela página, e o
	// texto é distinto o bastante para ser encontrado no JSON inteiro.
	m := criarMonitor(t, s, "api-prod-us-east-1", "http://banco-interno.vpc.local:5432/health")

	inc := domain.Incident{
		MonitorID: m.ID,
		StartedAt: agora.Add(-time.Hour),
		Cause:     `dial tcp 10.0.3.7:5432: connect: connection refused`,
	}
	if err := s.Incidents().Open(ctx, &inc); err != nil {
		t.Fatalf("abrindo incidente: %v", err)
	}

	if err := s.WriteHeartbeats(ctx, []domain.Heartbeat{{
		MonitorID: m.ID,
		Timestamp: agora.Add(-time.Minute),
		Status:    domain.StatusDown,
		Message:   `Get "http://banco-interno.vpc.local:5432/health": context deadline exceeded`,
	}}); err != nil {
		t.Fatalf("gravando batida: %v", err)
	}

	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID, Label: "API"})

	a := domain.Announcement{
		Title: "Instabilidade na API", Impact: domain.ImpactMajor,
		Phase: domain.PhaseInvestigating, Components: []int64{m.ID}, StartedAt: agora.Add(-time.Hour),
	}
	if err := s.Announcements().Create(ctx, &a); err != nil {
		t.Fatalf("criando relato: %v", err)
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando página: %v", err)
	}

	bruto, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("serializando: %v", err)
	}
	texto := string(bruto)

	proibidos := map[string]string{
		"endereço do alvo":        "banco-interno.vpc.local",
		"porta interna":           "5432",
		"causa detectada":         "connection refused",
		"mensagem do check":       "context deadline exceeded",
		"nome interno do monitor": "api-prod-us-east-1",
		"ip interno":              "10.0.3.7",
	}
	for oque, agulha := range proibidos {
		if strings.Contains(texto, agulha) {
			t.Errorf("página pública revelou %s (%q):\n%s", oque, agulha, texto)
		}
	}

	// E o que deveria sair, saiu: sem isto o teste passaria com uma
	// página vazia.
	if !strings.Contains(texto, "API") || !strings.Contains(texto, "Instabilidade na API") {
		t.Errorf("página não trouxe o conteúdo público esperado:\n%s", texto)
	}
}

// ---------- resolução da página ----------

func TestBuildPaginaInexistenteEhNotFound(t *testing.T) {
	s := newStore(t)

	_, err := newBuilder(s).Build(context.Background(), "nao-existe")

	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

func TestBuildPaginaDesligadaEhNotFound(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	p := criarPagina(t, s, "estado")
	p.Enabled = false
	if err := s.StatusPages().Update(ctx, p); err != nil {
		t.Fatalf("desligando: %v", err)
	}

	_, err := newBuilder(s).Build(ctx, "estado")

	// Não é um erro distinto de propósito. "Existe, mas está desligada"
	// confirmaria a existência da página a quem só chutou o endereço.
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperava ErrNotFound, veio %v", err)
	}
}

// ---------- componentes ----------

func TestBuildUsaORotuloPublico(t *testing.T) {
	s := newStore(t)

	m := criarMonitor(t, s, "api-prod-us-east-1", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID, Label: "API"})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	nome := view.Groups[0].Monitors[0].Name
	if nome != "API" {
		t.Fatalf("esperava o rótulo público, veio %q", nome)
	}
}

func TestBuildCaiNoNomeDoMonitorSemRotulo(t *testing.T) {
	// Sem rótulo, o nome interno é o menos ruim: uma linha em branco na
	// página seria pior do que um nome pouco elegante.
	s := newStore(t)

	m := criarMonitor(t, s, "loja", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if view.Groups[0].Monitors[0].Name != "loja" {
		t.Fatalf("esperava o nome do monitor, veio %q", view.Groups[0].Monitors[0].Name)
	}
}

func TestBuildAgrupaNaOrdemEditorial(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	p := criarPagina(t, s, "estado")
	solto := criarMonitor(t, s, "solto", "https://exemplo.com/a")
	naApi := criarMonitor(t, s, "na-api", "https://exemplo.com/b")
	noConsole := criarMonitor(t, s, "no-console", "https://exemplo.com/c")

	console := domain.StatusPageGroup{PageID: p.ID, Name: "Console", Position: 2}
	api := domain.StatusPageGroup{PageID: p.ID, Name: "API", Position: 1}
	for _, g := range []*domain.StatusPageGroup{&console, &api} {
		if err := s.StatusPages().CreateGroup(ctx, g); err != nil {
			t.Fatalf("criando grupo: %v", err)
		}
	}

	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: solto.ID})
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: naApi.ID, GroupID: &api.ID})
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: noConsole.ID, GroupID: &console.ID})

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if len(view.Groups) != 3 {
		t.Fatalf("esperava 3 grupos, vieram %d: %+v", len(view.Groups), view.Groups)
	}
	// Os sem grupo vêm primeiro, num grupo sem nome; depois os grupos na
	// ordem que quem publica escolheu.
	if view.Groups[0].Name != "" || view.Groups[0].Monitors[0].Name != "solto" {
		t.Errorf("grupo implícito divergiu: %+v", view.Groups[0])
	}
	if view.Groups[1].Name != "API" || view.Groups[2].Name != "Console" {
		t.Errorf("ordem editorial divergiu: %q, %q", view.Groups[1].Name, view.Groups[2].Name)
	}
}

func TestBuildOmiteGrupoVazio(t *testing.T) {
	// Grupo sem componente é uma seção com título e nada embaixo; quem lê
	// fica procurando o que não está lá.
	s := newStore(t)
	ctx := context.Background()

	p := criarPagina(t, s, "estado")
	m := criarMonitor(t, s, "api", "https://exemplo.com")
	vazio := domain.StatusPageGroup{PageID: p.ID, Name: "Sem nada"}
	if err := s.StatusPages().CreateGroup(ctx, &vazio); err != nil {
		t.Fatalf("criando grupo: %v", err)
	}
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	for _, g := range view.Groups {
		if g.Name == "Sem nada" {
			t.Fatal("grupo vazio apareceu na página")
		}
	}
}

// ---------- barras e estado ----------

func TestBuildMontaUmaBarraPorDia(t *testing.T) {
	s := newStore(t)

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})

	gravarDia(t, s, m.ID, agora.AddDate(0, 0, -1), 1440, 0)
	gravarDia(t, s, m.ID, agora, 900, 0)

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	hist := view.Groups[0].Monitors[0].History
	if len(hist) != view.WindowDays {
		t.Fatalf("esperava %d barras, vieram %d", view.WindowDays, len(hist))
	}
	// Do mais antigo ao mais recente: a barra da direita é hoje, como em
	// toda linha do tempo que se lê da esquerda para a direita.
	if hist[0].Date >= hist[len(hist)-1].Date {
		t.Fatal("barras fora de ordem cronológica")
	}
	if hist[len(hist)-1].Date != agora.Format("2006-01-02") {
		t.Fatalf("última barra não é hoje: %q", hist[len(hist)-1].Date)
	}
}

func TestBuildDiaSemDadoFicaSemMedicao(t *testing.T) {
	// Instalação nova tem noventa barras e dado em uma. As outras
	// oitenta e nove não são quedas — são dias em que ninguém mediu, e
	// pintá-las de vermelho inventaria um histórico de indisponibilidade.
	s := newStore(t)

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})
	gravarDia(t, s, m.ID, agora, 1440, 0)

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	hist := view.Groups[0].Monitors[0].History
	if hist[0].Status != domain.StatusUnknown {
		t.Errorf("dia sem dado virou %s", hist[0].Status)
	}
	if hist[0].UptimePercent != nil {
		t.Errorf("dia sem dado ganhou percentual: %v", *hist[0].UptimePercent)
	}
	if hist[len(hist)-1].Status != domain.StatusUp {
		t.Errorf("dia com dado divergiu: %s", hist[len(hist)-1].Status)
	}
}

func TestBuildDiaComQualquerQuedaEhQueda(t *testing.T) {
	s := newStore(t)

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})
	gravarDia(t, s, m.ID, agora, 1430, 10)

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	hist := view.Groups[0].Monitors[0].History
	ultimo := hist[len(hist)-1]
	if ultimo.Status != domain.StatusDown {
		t.Errorf("dia com queda parcial virou %s", ultimo.Status)
	}
	if ultimo.UptimePercent == nil {
		t.Fatal("dia medido ficou sem percentual")
	}
	if got := *ultimo.UptimePercent; got < 99.2 || got > 99.4 {
		t.Errorf("percentual do dia divergiu: %v", got)
	}
}

func TestBuildTopoTomaOPiorComponente(t *testing.T) {
	s := newStore(t)

	saudavel := criarMonitor(t, s, "saudavel", "https://exemplo.com/a")
	caido := criarMonitor(t, s, "caido", "https://exemplo.com/b")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: saudavel.ID})
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: caido.ID})

	gravarDia(t, s, saudavel.ID, agora, 1440, 0)
	gravarDia(t, s, caido.ID, agora, 0, 1440)

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if view.Status != domain.StatusDown {
		t.Fatalf("topo esperava down, veio %s", view.Status)
	}
}

func TestBuildLatenciaSoApareceQuandoLiberada(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})

	r := domain.Rollup{
		MonitorID: m.ID, ProbeID: "local", Resolution: domain.ResolutionDaily,
		BucketStart: agora.UTC().Truncate(24 * time.Hour),
		Total:       100, Up: 100, LatencyP95MS: 187,
	}
	if err := s.WriteRollups(ctx, []domain.Rollup{r}); err != nil {
		t.Fatalf("gravando agregado: %v", err)
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}
	if view.Groups[0].Monitors[0].LatencyP95Ms != nil {
		t.Error("latência apareceu numa página que não a liberou")
	}

	p.ShowLatency = true
	if err := s.StatusPages().Update(ctx, p); err != nil {
		t.Fatalf("liberando latência: %v", err)
	}

	view, err = newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}
	p95 := view.Groups[0].Monitors[0].LatencyP95Ms
	if p95 == nil || *p95 != 187 {
		t.Fatalf("latência liberada divergiu: %v", p95)
	}
}

func TestBuildUptimeAusenteSemMedicaoAlguma(t *testing.T) {
	s := newStore(t)

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID})

	view, err := newBuilder(s).Build(context.Background(), "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	mon := view.Groups[0].Monitors[0]
	if mon.UptimePercent != nil {
		t.Errorf("alvo sem medição ganhou percentual: %v", *mon.UptimePercent)
	}
	if mon.Status != domain.StatusUnknown {
		t.Errorf("alvo sem medição virou %s", mon.Status)
	}
}

// ---------- relatos ----------

func TestBuildTrazRelatoDoComponentePublicado(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	m := criarMonitor(t, s, "api", "https://exemplo.com")
	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: m.ID, Label: "API"})

	a := domain.Announcement{
		Title: "Lentidão", Impact: domain.ImpactMinor, Phase: domain.PhaseIdentified,
		Components: []int64{m.ID}, StartedAt: agora.Add(-2 * time.Hour),
	}
	if err := s.Announcements().Create(ctx, &a); err != nil {
		t.Fatalf("criando relato: %v", err)
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if len(view.Announcements) != 1 {
		t.Fatalf("esperava 1 relato, vieram %d", len(view.Announcements))
	}
	// O componente aparece pelo rótulo público: o número não diz nada a
	// quem lê, e revelaria quantos monitores a instalação tem.
	if len(view.Announcements[0].Components) != 1 || view.Announcements[0].Components[0] != "API" {
		t.Errorf("componentes do relato divergiram: %+v", view.Announcements[0].Components)
	}
	// O topo reflete o relato aberto, não só as sondas.
	if view.Impact != domain.ImpactMinor {
		t.Errorf("impacto do topo divergiu: %s", view.Impact)
	}
}

func TestBuildNaoTrazRelatoDeOutraPagina(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	daPagina := criarMonitor(t, s, "meu", "https://exemplo.com/a")
	deOutroCliente := criarMonitor(t, s, "de-outro", "https://exemplo.com/b")

	p := criarPagina(t, s, "estado")
	publicar(t, s, domain.StatusPageComponent{PageID: p.ID, MonitorID: daPagina.ID})

	a := domain.Announcement{
		Title: "Queda no ambiente do outro cliente", Impact: domain.ImpactCritical,
		Phase: domain.PhaseInvestigating, Components: []int64{deOutroCliente.ID}, StartedAt: agora,
	}
	if err := s.Announcements().Create(ctx, &a); err != nil {
		t.Fatalf("criando relato: %v", err)
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	// O pior tipo de vazamento numa página pública é informação sobre
	// terceiro.
	if len(view.Announcements) != 0 {
		t.Fatalf("relato de outro cliente apareceu: %+v", view.Announcements)
	}
	if view.Impact != domain.ImpactNone {
		t.Errorf("impacto de outro cliente contaminou o topo: %s", view.Impact)
	}
}

func TestBuildTrazRelatoGlobalEmQualquerPagina(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	p := criarPagina(t, s, "estado")
	a := domain.Announcement{
		Title: "Manutenção programada", Impact: domain.ImpactNone,
		Phase: domain.PhaseMonitoring, Global: true, StartedAt: agora,
	}
	if err := s.Announcements().Create(ctx, &a); err != nil {
		t.Fatalf("criando relato: %v", err)
	}
	_ = p

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if len(view.Announcements) != 1 {
		t.Fatalf("relato global não apareceu: %+v", view.Announcements)
	}
}

func TestBuildTrazLinhaDoTempoEmOrdem(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	criarPagina(t, s, "estado")
	a := domain.Announcement{
		Title: "Falha", Impact: domain.ImpactMajor, Phase: domain.PhaseResolved,
		Global: true, StartedAt: agora.Add(-3 * time.Hour),
	}
	if err := s.Announcements().Create(ctx, &a); err != nil {
		t.Fatalf("criando relato: %v", err)
	}

	for _, u := range []domain.AnnouncementUpdate{
		{AnnouncementID: a.ID, Phase: domain.PhaseResolved, Body: "Normalizado.", PublishedAt: agora.Add(-time.Hour)},
		{AnnouncementID: a.ID, Phase: domain.PhaseInvestigating, Body: "Investigando.", PublishedAt: agora.Add(-3 * time.Hour)},
	} {
		upd := u
		if err := s.Announcements().AddUpdate(ctx, &upd); err != nil {
			t.Fatalf("publicando: %v", err)
		}
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	updates := view.Announcements[0].Updates
	if len(updates) != 2 {
		t.Fatalf("esperava 2 atualizações, vieram %d", len(updates))
	}
	if updates[0].Body != "Investigando." {
		t.Errorf("linha do tempo fora de ordem: %+v", updates)
	}
}

func TestBuildIgnoraRelatoForaDaJanela(t *testing.T) {
	// A página cobre noventa dias. Devolver o histórico inteiro a cada
	// visita anônima é como uma instalação antiga vira alvo fácil.
	s := newStore(t)
	ctx := context.Background()

	criarPagina(t, s, "estado")
	antigo := domain.Announcement{
		Title: "Do ano passado", Impact: domain.ImpactMajor, Phase: domain.PhaseResolved,
		Global: true, StartedAt: agora.AddDate(-1, 0, 0),
	}
	if err := s.Announcements().Create(ctx, &antigo); err != nil {
		t.Fatalf("criando relato: %v", err)
	}

	view, err := newBuilder(s).Build(ctx, "estado")
	if err != nil {
		t.Fatalf("montando: %v", err)
	}

	if len(view.Announcements) != 0 {
		t.Fatalf("relato fora da janela apareceu: %+v", view.Announcements)
	}
}
