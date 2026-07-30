// Package statuspage monta a página pública de estado.
//
// É a única superfície do UpWatch alcançada sem credencial, e por isso a
// montagem vive isolada num pacote próprio em vez de dentro do handler.
// Um lugar só decide o que sai, um teste só cobre esse lugar, e nenhum
// handler futuro pode montar a resposta "quase igual" com um campo a
// mais.
//
// O que não sai daqui, e por quê: o endereço do alvo e a mensagem do
// check descrevem a topologia interna — "banco-interno.vpc.local:5432",
// "dial tcp 10.0.3.7:5432: connection refused". Quem abriu o link quer
// saber se o serviço está no ar, não em qual porta ele escuta.
package statuspage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// WindowDays é quanto histórico a página mostra.
//
// Noventa dias é a convenção da categoria, e cabe na retenção diária
// padrão com folga de sobra.
const WindowDays = 90

// dateLayout é o formato do dia de calendário nas barras.
const dateLayout = "2006-01-02"

// Builder monta a visão pública a partir do store.
type Builder struct {
	store store.Store
	clock clock.Clock
}

// NewBuilder cria o montador.
func NewBuilder(s store.Store, c clock.Clock) *Builder {
	if c == nil {
		c = clock.Real()
	}
	return &Builder{store: s, clock: c}
}

// Build monta a página do slug.
//
// Devolve store.ErrNotFound tanto para página inexistente quanto para
// página desligada. Distinguir as duas confirmaria a existência da
// página a quem só chutou o endereço.
func (b *Builder) Build(ctx context.Context, slug string) (domain.PublicView, error) {
	page, err := b.store.StatusPages().GetBySlug(ctx, slug)
	if err != nil {
		return domain.PublicView{}, err
	}
	return b.build(ctx, page)
}

// BuildDefault monta a página que responde em "/status".
//
// Devolve store.ErrNotFound quando nenhuma foi marcada como padrão — a
// instalação não elege uma sozinha, porque adivinhar qual publicar seria
// pior do que não publicar.
func (b *Builder) BuildDefault(ctx context.Context) (domain.PublicView, error) {
	page, err := b.store.StatusPages().GetDefault(ctx)
	if err != nil {
		return domain.PublicView{}, err
	}
	return b.build(ctx, page)
}

func (b *Builder) build(ctx context.Context, page domain.StatusPage) (domain.PublicView, error) {
	if !page.Enabled {
		return domain.PublicView{}, fmt.Errorf("página de estado %q: %w", page.Slug, store.ErrNotFound)
	}

	componentes, err := b.store.StatusPages().Components(ctx, page.ID)
	if err != nil {
		return domain.PublicView{}, err
	}
	grupos, err := b.store.StatusPages().Groups(ctx, page.ID)
	if err != nil {
		return domain.PublicView{}, err
	}

	agora := b.clock.Now().UTC()
	desde := agora.AddDate(0, 0, -(WindowDays - 1)).Truncate(24 * time.Hour)

	// rotulos serve dois propósitos: montar as linhas e traduzir os
	// componentes de um relato em nomes públicos.
	rotulos := map[int64]string{}
	publicos := map[int64]domain.PublicMonitor{}

	for _, c := range componentes {
		m, err := b.store.Monitors().Get(ctx, c.MonitorID)
		if err != nil {
			return domain.PublicView{}, err
		}

		nome := c.Label
		if nome == "" {
			// Sem rótulo, o nome interno é o menos ruim: uma linha em
			// branco seria pior que um nome pouco elegante.
			nome = m.Name
		}
		rotulos[c.MonitorID] = nome

		pm, err := b.monitor(ctx, c.MonitorID, nome, page.ShowLatency, desde, agora)
		if err != nil {
			return domain.PublicView{}, err
		}
		publicos[c.MonitorID] = pm
	}

	view := domain.PublicView{
		Slug:        page.Slug,
		Title:       page.Title,
		Description: page.Description,
		TimeZone:    page.TimeZone,
		WindowDays:  WindowDays,
		GeneratedAt: agora,
		Groups:      assemble(componentes, grupos, publicos),
	}

	estados := make([]domain.Status, 0, len(publicos))
	for _, c := range componentes {
		estados = append(estados, publicos[c.MonitorID].Status)
	}
	view.Status = domain.OverallStatus(estados)

	view.Announcements, err = b.announcements(ctx, componentes, rotulos, desde)
	if err != nil {
		return domain.PublicView{}, err
	}
	view.Impact = domain.WorstOpenImpact(view.Announcements)

	return view, nil
}

// monitor monta uma linha da página.
func (b *Builder) monitor(
	ctx context.Context,
	monitorID int64,
	nome string,
	showLatency bool,
	desde, agora time.Time,
) (domain.PublicMonitor, error) {
	rollups, err := b.store.QueryRollups(ctx, store.RollupQuery{
		MonitorID:  monitorID,
		Resolution: domain.ResolutionDaily,
		Range:      store.TimeRange{From: desde, To: agora.Add(24 * time.Hour)},
	})
	if err != nil {
		return domain.PublicMonitor{}, err
	}

	porDia := make(map[string]domain.Rollup, len(rollups))
	for _, r := range rollups {
		porDia[r.BucketStart.UTC().Format(dateLayout)] = r
	}

	pm := domain.PublicMonitor{Name: nome, History: make([]domain.PublicDay, 0, WindowDays)}

	var observadas, foraDoAr int
	var piorP95 float64

	for i := 0; i < WindowDays; i++ {
		dia := desde.AddDate(0, 0, i)
		data := dia.Format(dateLayout)

		r, ok := porDia[data]
		if !ok || r.Observed() == 0 {
			// Dia sem medição não é dia de queda. Numa instalação nova são
			// oitenta e nove barras assim, e pintá-las inventaria um
			// histórico de indisponibilidade que nunca existiu.
			pm.History = append(pm.History, domain.PublicDay{Date: data, Status: domain.StatusUnknown})
			continue
		}

		pct := r.UptimePercent()
		pm.History = append(pm.History, domain.PublicDay{
			Date:          data,
			Status:        dayStatus(r),
			UptimePercent: &pct,
		})

		observadas += r.Observed()
		foraDoAr += r.Down
		if r.LatencyP95MS > piorP95 {
			piorP95 = r.LatencyP95MS
		}
	}

	if observadas > 0 {
		pct := float64(observadas-foraDoAr) / float64(observadas) * 100
		pm.UptimePercent = &pct
	}
	if showLatency && piorP95 > 0 {
		ms := int64(piorP95 + 0.5)
		pm.LatencyP95Ms = &ms
	}

	// O estado atual é o do dia mais recente com medição, varrendo de trás
	// para frente: um alvo verificado às 3h da manhã e não mais depois
	// continua descrevendo o que se sabe dele.
	pm.Status = domain.StatusUnknown
	for i := len(pm.History) - 1; i >= 0; i-- {
		if pm.History[i].Status != domain.StatusUnknown {
			pm.Status = pm.History[i].Status
			break
		}
	}

	return pm, nil
}

// dayStatus resume um dia agregado.
//
// Qualquer queda no dia domina: vinte e três horas e meia no ar não é um
// dia verde, e é justamente a meia hora que faltou que se procura ao
// olhar a barra.
func dayStatus(r domain.Rollup) domain.Status {
	switch {
	case r.Down > 0:
		return domain.StatusDown
	case r.Degraded > 0:
		return domain.StatusDegraded
	case r.Up > 0:
		return domain.StatusUp
	default:
		return domain.StatusUnknown
	}
}

// assemble distribui os componentes nos grupos, na ordem editorial.
//
// Os sem grupo vêm primeiro, num grupo de nome vazio, para o cliente ter
// um laço só em vez de uma lista à parte que ele precise lembrar de
// desenhar antes.
func assemble(
	componentes []domain.StatusPageComponent,
	grupos []domain.StatusPageGroup,
	publicos map[int64]domain.PublicMonitor,
) []domain.PublicGroup {
	porGrupo := map[int64][]domain.PublicMonitor{}
	var soltos []domain.PublicMonitor

	// componentes já vem ordenado por posição, e a distribuição preserva
	// essa ordem dentro de cada grupo.
	for _, c := range componentes {
		pm := publicos[c.MonitorID]
		if c.GroupID == nil {
			soltos = append(soltos, pm)
			continue
		}
		porGrupo[*c.GroupID] = append(porGrupo[*c.GroupID], pm)
	}

	out := []domain.PublicGroup{}
	if len(soltos) > 0 {
		out = append(out, domain.PublicGroup{Monitors: soltos})
	}

	sort.SliceStable(grupos, func(i, j int) bool { return grupos[i].Position < grupos[j].Position })
	for _, g := range grupos {
		membros := porGrupo[g.ID]
		if len(membros) == 0 {
			// Grupo vazio é uma seção com título e nada embaixo; quem lê
			// fica procurando o que não está lá.
			continue
		}
		out = append(out, domain.PublicGroup{Name: g.Name, Monitors: membros})
	}
	return out
}

// announcements traz os relatos que esta página deve mostrar.
func (b *Builder) announcements(
	ctx context.Context,
	componentes []domain.StatusPageComponent,
	rotulos map[int64]string,
	desde time.Time,
) ([]domain.PublicAnnouncement, error) {
	publicados := make([]int64, 0, len(componentes))
	for _, c := range componentes {
		publicados = append(publicados, c.MonitorID)
	}

	page, err := b.store.Announcements().List(ctx, store.AnnouncementFilter{Since: desde})
	if err != nil {
		return nil, err
	}

	out := []domain.PublicAnnouncement{}
	for _, a := range page.Items {
		// A regra de alcance é do domínio e está coberta por teste lá: um
		// relato aparece onde ao menos um componente afetado é publicado,
		// ou quando é da plataforma inteira. Página de um cliente nunca
		// mostra a queda de outro.
		if !a.ShowsOn(publicados) {
			continue
		}

		updates, err := b.store.Announcements().Updates(ctx, a.ID)
		if err != nil {
			return nil, err
		}

		pa := domain.PublicAnnouncement{
			Title:      a.Title,
			Impact:     a.Impact,
			Phase:      a.Phase,
			StartedAt:  a.StartedAt,
			ResolvedAt: a.ResolvedAt,
			Components: publicLabels(a.Components, rotulos),
			Updates:    make([]domain.PublicAnnouncementUpdate, 0, len(updates)),
		}
		for _, u := range updates {
			pa.Updates = append(pa.Updates, domain.PublicAnnouncementUpdate{
				Phase: u.Phase, Body: u.Body, PublishedAt: u.PublishedAt,
			})
		}
		out = append(out, pa)
	}
	return out, nil
}

// publicLabels traduz identificadores em nomes públicos.
//
// Componente afetado que esta página não publica fica de fora da lista: o
// nome dele não é assunto de quem lê esta página, e o identificador cru
// revelaria quantos monitores a instalação tem.
func publicLabels(ids []int64, rotulos map[int64]string) []string {
	var out []string
	for _, id := range ids {
		if nome, ok := rotulos[id]; ok {
			out = append(out, nome)
		}
	}
	return out
}
