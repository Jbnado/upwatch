package incident_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
	"github.com/Jbnado/upwatch/internal/maintenance"
	"github.com/Jbnado/upwatch/internal/store"
)

// emManutencao declara a janela [epoch, epoch+duracao) sobre os monitores
// e liga o motor ao calendário.
func (f *familia) emManutencao(t *testing.T, duracao time.Duration, monitores ...int64) {
	t.Helper()
	ctx := context.Background()
	w := domain.MaintenanceWindow{
		Title: "deploy da sonda", StartsAt: epoch, EndsAt: epoch.Add(duracao),
		Recurrence: domain.RecurrenceNone, TimeZone: "UTC", MonitorIDs: monitores,
	}
	if err := f.store.Maintenance().Create(ctx, &w); err != nil {
		t.Fatalf("Maintenance().Create returned unexpected error: %v", err)
	}
	cal := maintenance.New(f.store)
	if err := cal.Reload(ctx); err != nil {
		t.Fatalf("Reload returned unexpected error: %v", err)
	}
	f.engine.WithMaintenance(cal)
}

func (f *familia) avisos(m domain.Monitor) []domain.ChangeKind {
	var out []domain.ChangeKind
	for _, aviso := range f.dispatch.all() {
		if aviso.Monitor.ID == m.ID {
			out = append(out, aviso.Event.Kind)
		}
	}
	return out
}

// Caso real: o deploy derruba a API por um minuto, e a lâmpada do time
// inteiro acendia por nada. Dentro da janela a verificação continua e é
// gravada, mas a queda não avisa, não abre incidente e não conta contra a
// disponibilidade — vai para o histórico como lacuna, dizendo por quê.
func TestEngineMaintenanceSilencesTheOutage(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.emManutencao(t, 30*time.Minute, f.pai.ID)

	for i := range threshold + 2 {
		f.bate(t, f.pai, i, domain.StatusDown)
	}

	if got := f.avisos(f.pai); len(got) != 0 {
		t.Errorf("notifications during maintenance = %v, want none", got)
	}
	if _, err := f.store.Incidents().Current(context.Background(), f.pai.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("incident during maintenance = %v, want none", err)
	}
	gravada := f.ultimaGravada(t, f.pai)
	if gravada.Status != domain.StatusUnknown {
		t.Errorf("heartbeat stored as %s, want unknown (a gap, not an outage)", gravada.Status)
	}
	if !strings.Contains(gravada.Message, "em manutenção") || !strings.Contains(gravada.Message, "deploy da sonda") ||
		!strings.Contains(gravada.Message, "connection refused") {
		t.Errorf("heartbeat message = %q, want the maintenance, its title and the original cause", gravada.Message)
	}
}

// A janela sobre o pai vale para o filho: a queda dele é a do pai, e
// avisá-la seria acender pelo dependente o aviso que a janela apagou.
func TestEngineMaintenanceOnTheParentCoversDependents(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.emManutencao(t, 30*time.Minute, f.pai.ID)

	for i := range threshold + 2 {
		f.bate(t, f.pai, i, domain.StatusDown)
		f.bate(t, f.filho, i, domain.StatusDown)
	}

	if got := f.avisos(f.filho); len(got) != 0 {
		t.Errorf("child notifications during the parent's maintenance = %v, want none", got)
	}
	if msg := f.ultimaGravada(t, f.filho).Message; !strings.Contains(msg, "em manutenção") {
		t.Errorf("child heartbeat message = %q, want it to name the maintenance", msg)
	}
}

// Decisão: o alvo que continua fora quando a janela acaba avisa — uma
// vez, depois das confirmações de sempre, como uma queda nova. Calar até
// alguém olhar transformaria a janela no monitor desligado que ninguém
// religou.
func TestEngineMaintenanceEndingWithTheTargetDownNotifiesOnce(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.emManutencao(t, 10*time.Minute, f.pai.ID)

	for i := range 10 {
		f.bate(t, f.pai, i, domain.StatusDown)
	}
	if got := f.avisos(f.pai); len(got) != 0 {
		t.Fatalf("notifications inside the window = %v, want none", got)
	}

	for i := range threshold - 1 {
		f.bate(t, f.pai, 10+i, domain.StatusDown)
	}
	if got := f.avisos(f.pai); len(got) != 0 {
		t.Errorf("notifications before confirming after the window = %v, want none yet", got)
	}

	for i := range 5 {
		f.bate(t, f.pai, 10+threshold-1+i, domain.StatusDown)
	}
	if got := f.avisos(f.pai); len(got) != 1 || got[0] != incident.KindDown {
		t.Errorf("notifications after the window = %v, want exactly one down", got)
	}
	if _, err := f.store.Incidents().Current(context.Background(), f.pai.ID); err != nil {
		t.Errorf("incident after the window = %v, want one open", err)
	}
}

// Degradação também avisa, então também fica calada na janela.
func TestEngineMaintenanceSilencesDegradation(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.emManutencao(t, 30*time.Minute, f.pai.ID)

	for i := range threshold + 2 {
		f.bate(t, f.pai, i, domain.StatusDegraded)
	}
	if got := f.avisos(f.pai); len(got) != 0 {
		t.Errorf("notifications for degradation during maintenance = %v, want none", got)
	}
}

// A queda confirmada antes da janela já avisou; saber que ela acabou é
// notícia, com ou sem janela.
func TestEngineMaintenanceKeepsTheRecoveryOfAnEarlierOutage(t *testing.T) {
	f := novaFamilia(t)
	for i := range threshold {
		f.bate(t, f.pai, i-20, domain.StatusUp)
	}
	for i := range threshold {
		f.bate(t, f.pai, i-10, domain.StatusDown)
	}
	f.emManutencao(t, 30*time.Minute, f.pai.ID)

	for i := range threshold {
		f.bate(t, f.pai, i, domain.StatusUp)
	}
	got := f.avisos(f.pai)
	if len(got) != 2 || got[1] != incident.KindUp {
		t.Errorf("notifications = %v, want the earlier down and then the recovery", got)
	}
}

// Fora do alvo da janela nada muda.
func TestEngineMaintenanceLeavesOtherMonitorsAlone(t *testing.T) {
	f := novaFamilia(t)
	f.ambosNoAr(t)
	f.emManutencao(t, 30*time.Minute, f.filho.ID)

	for i := range threshold {
		f.bate(t, f.pai, i, domain.StatusDown)
	}
	if got := f.avisos(f.pai); len(got) != 1 {
		t.Errorf("parent notifications with a window only on the child = %v, want one down", got)
	}
}
