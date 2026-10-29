package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

func janela(inicio time.Time, dur time.Duration, rec domain.Recurrence) domain.MaintenanceWindow {
	return domain.MaintenanceWindow{
		Title:      "deploy",
		StartsAt:   inicio,
		EndsAt:     inicio.Add(dur),
		Recurrence: rec,
		TimeZone:   "America/Sao_Paulo",
		MonitorIDs: []int64{1},
	}
}

func saoPaulo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation returned unexpected error: %v", err)
	}
	return loc
}

func TestMaintenanceValidateRefusesWhatWouldSilenceForever(t *testing.T) {
	inicio := time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)
	casos := map[string]struct {
		w     domain.MaintenanceWindow
		campo string
	}{
		"sem título": {func() domain.MaintenanceWindow {
			w := janela(inicio, time.Hour, domain.RecurrenceNone)
			w.Title = " "
			return w
		}(), "title"},
		"fim antes do início": {janela(inicio, -time.Minute, domain.RecurrenceNone), "ends_at"},
		// Uma ocorrência diária de 24h é manutenção permanente.
		"diária do tamanho do dia": {janela(inicio, 24*time.Hour, domain.RecurrenceDaily), "ends_at"},
		"semanal de oito dias":     {janela(inicio, 8*24*time.Hour, domain.RecurrenceWeekly), "ends_at"},
		// A janela esquecida é o monitor desligado que ninguém religou.
		"avulsa de dois meses":  {janela(inicio, 60*24*time.Hour, domain.RecurrenceNone), "ends_at"},
		"recorrência inventada": {janela(inicio, time.Hour, "monthly"), "recurrence"},
		"fuso inventado": {func() domain.MaintenanceWindow {
			w := janela(inicio, time.Hour, domain.RecurrenceNone)
			w.TimeZone = "America/Atlantida"
			return w
		}(), "time_zone"},
		"sem alvo": {func() domain.MaintenanceWindow {
			w := janela(inicio, time.Hour, domain.RecurrenceNone)
			w.MonitorIDs = nil
			return w
		}(), "monitor_ids"},
	}
	for nome, c := range casos {
		t.Run(nome, func(t *testing.T) {
			var verr *domain.ValidationError
			err := c.w.Validate()
			if !errors.As(err, &verr) || verr.Field != c.campo {
				t.Fatalf("Validate() = %v, want a validation error on %q", err, c.campo)
			}
		})
	}

	if err := janela(inicio, 23*time.Hour, domain.RecurrenceDaily).Validate(); err != nil {
		t.Errorf("Validate() on a valid daily window = %v, want nil", err)
	}
}

func TestMaintenanceOneOffIsHalfOpen(t *testing.T) {
	inicio := time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)
	w := janela(inicio, 30*time.Minute, domain.RecurrenceNone)

	casos := []struct {
		em   time.Time
		quer bool
	}{
		{inicio.Add(-time.Second), false},
		{inicio, true},
		{inicio.Add(29 * time.Minute), true},
		// No fim exato a janela já acabou: a verificação das 22h30 é a
		// primeira que volta a contar.
		{inicio.Add(30 * time.Minute), false},
		{inicio.Add(25 * time.Hour), false},
	}
	for _, c := range casos {
		if got := w.ActiveAt(c.em); got != c.quer {
			t.Errorf("ActiveAt(%s) = %v, want %v", c.em, got, c.quer)
		}
	}
}

func TestMaintenanceWeeklyRepeatsTheWallClockAcrossDST(t *testing.T) {
	// Domingo, 02h–03h em São Paulo. A mesma janela em Nova York cruzaria
	// a troca de horário; São Paulo não tem horário de verão hoje, então
	// o teste usa Nova York para provar que o horário de parede se mantém.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation returned unexpected error: %v", err)
	}
	inicio := time.Date(2026, 10, 25, 2, 0, 0, 0, ny) // domingo, EDT
	w := janela(inicio, time.Hour, domain.RecurrenceWeekly)
	w.TimeZone = "America/New_York"

	// 1º de novembro de 2026 é a volta para EST: duas e meia da manhã de
	// parede no domingo seguinte à troca precisa estar coberta.
	depois := time.Date(2026, 11, 8, 2, 30, 0, 0, ny)
	if !w.ActiveAt(depois) {
		t.Errorf("ActiveAt(%s) = false, want true: the wall clock moved with DST", depois)
	}
	// Uma hora mais cedo, no fuso absoluto que valia antes da troca, já
	// não é manutenção.
	if w.ActiveAt(time.Date(2026, 11, 8, 1, 30, 0, 0, ny)) {
		t.Error("ActiveAt one hour before the wall-clock window = true, want false")
	}
	// Segunda-feira no mesmo horário não é domingo.
	if w.ActiveAt(time.Date(2026, 11, 9, 2, 30, 0, 0, ny)) {
		t.Error("a weekly window covered a Monday")
	}
}

func TestMaintenanceDailyRepeatsAndStopsWhenEnded(t *testing.T) {
	sp := saoPaulo(t)
	inicio := time.Date(2026, 10, 1, 19, 0, 0, 0, sp)
	w := janela(inicio, 30*time.Minute, domain.RecurrenceDaily)

	dezDiasDepois := time.Date(2026, 10, 11, 19, 10, 0, 0, sp)
	if !w.ActiveAt(dezDiasDepois) {
		t.Fatalf("ActiveAt ten days later = false, want true")
	}

	// Encerrar no meio da ocorrência corta esta e cancela as seguintes.
	w.EndedAt = ptr(time.Date(2026, 10, 11, 19, 5, 0, 0, sp))
	if w.ActiveAt(dezDiasDepois) {
		t.Error("ActiveAt after the early end = true, want false")
	}
	if w.ActiveAt(time.Date(2026, 10, 12, 19, 10, 0, 0, sp)) {
		t.Error("an ended recurring window kept covering the next day")
	}
	if got := w.StateAt(time.Date(2026, 10, 12, 0, 0, 0, 0, sp)); got != domain.MaintenanceFinished {
		t.Errorf("StateAt after the end = %q, want finished", got)
	}
}

func TestMaintenanceNextOccurrenceAnnouncesBeforeAndDuring(t *testing.T) {
	sp := saoPaulo(t)
	inicio := time.Date(2026, 10, 4, 2, 0, 0, 0, sp) // domingo
	w := janela(inicio, time.Hour, domain.RecurrenceWeekly)

	// Na quarta, a próxima é o domingo seguinte.
	quarta := time.Date(2026, 10, 7, 12, 0, 0, 0, sp)
	o, ok := w.NextOccurrence(quarta)
	if !ok || !o.Start.Equal(time.Date(2026, 10, 11, 2, 0, 0, 0, sp)) {
		t.Fatalf("NextOccurrence(wednesday) = %v, %v; want sunday 02:00", o, ok)
	}
	if got := w.StateAt(quarta); got != domain.MaintenanceScheduled {
		t.Errorf("StateAt(wednesday) = %q, want scheduled", got)
	}

	// Durante, é a própria ocorrência em curso.
	durante := time.Date(2026, 10, 11, 2, 30, 0, 0, sp)
	o, ok = w.NextOccurrence(durante)
	if !ok || !o.Start.Equal(time.Date(2026, 10, 11, 2, 0, 0, 0, sp)) {
		t.Errorf("NextOccurrence(during) = %v, %v; want the current occurrence", o, ok)
	}

	// Cancelada antes de começar, a avulsa não tem próxima.
	avulsa := janela(inicio, time.Hour, domain.RecurrenceNone)
	avulsa.EndedAt = ptr(inicio.Add(-time.Hour))
	if _, ok := avulsa.NextOccurrence(inicio.Add(-2 * time.Hour)); ok {
		t.Error("a cancelled one-off window still has a next occurrence")
	}
}

// arvoreRPA monta RPA → {hml, prod} e Retaguarda → prod.
func arvoreRPA() domain.GroupTree {
	return domain.NewGroupTree([]domain.Group{
		{ID: 10, Name: "RPA"},
		{ID: 11, Name: "hml", ParentID: ptr[int64](10)},
		{ID: 12, Name: "prod", ParentID: ptr[int64](10)},
		{ID: 20, Name: "Retaguarda"},
		{ID: 21, Name: "prod", ParentID: ptr[int64](20)},
	})
}

func TestMaintenanceOnASystemCoversItsEnvironments(t *testing.T) {
	tree := arvoreRPA()
	w := janela(time.Now(), time.Hour, domain.RecurrenceNone)
	w.MonitorIDs = nil
	w.GroupIDs = []int64{10}

	nenhum := func(int64) (domain.Monitor, bool) { return domain.Monitor{}, false }
	if !w.Covers(domain.Monitor{ID: 1, GroupID: ptr[int64](12)}, nenhum, tree) {
		t.Error("a window on RPA did not cover a monitor in RPA/prod")
	}
	if w.Covers(domain.Monitor{ID: 2, GroupID: ptr[int64](21)}, nenhum, tree) {
		t.Error("a window on RPA covered Retaguarda/prod")
	}
	if w.Covers(domain.Monitor{ID: 3}, nenhum, tree) {
		t.Error("a window on a group covered an ungrouped monitor")
	}

	// A janela no ambiente não sobe para o sistema.
	w.GroupIDs = []int64{11}
	if w.Covers(domain.Monitor{ID: 1, GroupID: ptr[int64](12)}, nenhum, tree) {
		t.Error("a window on RPA/hml covered RPA/prod")
	}
}

// A queda do filho cujo pai está em manutenção é a queda do pai: avisá-la
// seria acender o aviso que a janela veio apagar, por outro caminho.
func TestMaintenanceCoversDependentsOfACoveredParent(t *testing.T) {
	r := novaRede()
	r.add(1, nil, domain.StatusUp)
	r.add(2, ptr[int64](1), domain.StatusUp)
	r.add(3, ptr[int64](2), domain.StatusUp)
	r.add(4, nil, domain.StatusUp)

	w := janela(time.Now(), time.Hour, domain.RecurrenceNone)
	w.MonitorIDs = []int64{1}

	if !w.Covers(r.monitores[3], r.lookup, domain.GroupTree{}) {
		t.Error("the grandchild of a covered monitor is not covered")
	}
	if w.Covers(r.monitores[4], r.lookup, domain.GroupTree{}) {
		t.Error("an unrelated monitor is covered")
	}

	// Ciclo já gravado não pode travar a subida.
	r.monitores[1] = domain.Monitor{ID: 1, ParentID: ptr[int64](3)}
	w.MonitorIDs = []int64{99}
	if w.Covers(r.monitores[3], r.lookup, domain.GroupTree{}) {
		t.Error("a cycle made an uncovered monitor covered")
	}
}

func TestMaintenanceForNeedsBothTheTargetAndTheClock(t *testing.T) {
	inicio := time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)
	w := janela(inicio, time.Hour, domain.RecurrenceNone)
	nenhum := func(int64) (domain.Monitor, bool) { return domain.Monitor{}, false }
	m := domain.Monitor{ID: 1}

	if _, ok := domain.MaintenanceFor(m, nenhum, []domain.MaintenanceWindow{w}, domain.GroupTree{}, inicio.Add(-time.Minute)); ok {
		t.Error("MaintenanceFor before the window = true")
	}
	if _, ok := domain.MaintenanceFor(m, nenhum, []domain.MaintenanceWindow{w}, domain.GroupTree{}, inicio.Add(time.Minute)); !ok {
		t.Error("MaintenanceFor during the window = false")
	}
	if _, ok := domain.MaintenanceFor(domain.Monitor{ID: 2}, nenhum, []domain.MaintenanceWindow{w}, domain.GroupTree{}, inicio.Add(time.Minute)); ok {
		t.Error("MaintenanceFor on another monitor = true")
	}
}
