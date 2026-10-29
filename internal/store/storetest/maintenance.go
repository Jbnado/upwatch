package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// maintenanceCases cobrem as janelas de manutenção.
func maintenanceCases() []conformanceCase {
	return []conformanceCase{
		{"MaintenanceRoundTrips", testMaintenanceRoundTrips},
		{"MaintenanceGetMissingReturnsNotFound", testMaintenanceGetMissingReturnsNotFound},
		{"MaintenanceUpdateReplacesTargetsAndKeepsTheEnd", testMaintenanceUpdateReplacesTargetsAndKeepsTheEnd},
		{"MaintenanceEndKeepsTheEarliest", testMaintenanceEndKeepsTheEarliest},
		{"MaintenanceRefusesMissingTargets", testMaintenanceRefusesMissingTargets},
		{"MaintenanceTargetsCascade", testMaintenanceTargetsCascade},
		{"MaintenanceDelete", testMaintenanceDelete},
	}
}

func newMaintenance(monitores, grupos []int64) domain.MaintenanceWindow {
	return domain.MaintenanceWindow{
		Title:      "deploy do RPA",
		Message:    "Atualização programada.",
		StartsAt:   epoch,
		EndsAt:     epoch.Add(30 * time.Minute),
		Recurrence: domain.RecurrenceWeekly,
		TimeZone:   "America/Sao_Paulo",
		MonitorIDs: monitores,
		GroupIDs:   grupos,
	}
}

func mustCreateMaintenance(t *testing.T, s store.Store, w domain.MaintenanceWindow) domain.MaintenanceWindow {
	t.Helper()
	if err := s.Maintenance().Create(context.Background(), &w); err != nil {
		t.Fatalf("Maintenance().Create returned unexpected error: %v", err)
	}
	return w
}

func testMaintenanceRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	rpa := mustCreateGroup(t, s, "RPA", nil)
	m := mustCreateMonitor(t, s, "api")
	w := mustCreateMaintenance(t, s, newMaintenance([]int64{m, m}, []int64{rpa.ID}))
	if w.ID == 0 || w.CreatedAt.IsZero() {
		t.Fatalf("Create did not fill ID and timestamps: %+v", w)
	}

	got, err := s.Maintenance().Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Title != w.Title || got.Message != w.Message || got.Recurrence != domain.RecurrenceWeekly ||
		got.TimeZone != "America/Sao_Paulo" || !got.StartsAt.Equal(w.StartsAt) || !got.EndsAt.Equal(w.EndsAt) ||
		got.EndedAt != nil {
		t.Errorf("Get = %+v, want the same definition as %+v", got, w)
	}
	if !slices.Equal(got.MonitorIDs, []int64{m}) || !slices.Equal(got.GroupIDs, []int64{rpa.ID}) {
		t.Errorf("targets = %v / %v, want [%d] / [%d]", got.MonitorIDs, got.GroupIDs, m, rpa.ID)
	}

	todas, err := s.Maintenance().List(ctx)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(todas) != 1 || !slices.Equal(todas[0].MonitorIDs, []int64{m}) {
		t.Errorf("List = %+v, want the one window with its targets", todas)
	}
}

func testMaintenanceGetMissingReturnsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.Maintenance().Get(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get(999) returned %v, want store.ErrNotFound", err)
	}
	if err := s.Maintenance().End(ctx, 999, epoch); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("End(999) returned %v, want store.ErrNotFound", err)
	}
	if err := s.Maintenance().Update(ctx, domain.MaintenanceWindow{ID: 999}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update(999) returned %v, want store.ErrNotFound", err)
	}
}

// Uma edição feita com a tela aberta desde antes do encerramento não pode
// reabrir a janela: o encerramento tem caminho próprio.
func testMaintenanceUpdateReplacesTargetsAndKeepsTheEnd(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	a := mustCreateMonitor(t, s, "a")
	b := mustCreateMonitor(t, s, "b")
	w := mustCreateMaintenance(t, s, newMaintenance([]int64{a}, nil))

	fim := epoch.Add(10 * time.Minute)
	if err := s.Maintenance().End(ctx, w.ID, fim); err != nil {
		t.Fatalf("End returned unexpected error: %v", err)
	}

	w.Title = "deploy do BFF"
	w.MonitorIDs = []int64{b}
	w.EndedAt = nil
	if err := s.Maintenance().Update(ctx, w); err != nil {
		t.Fatalf("Update returned unexpected error: %v", err)
	}

	got, err := s.Maintenance().Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.Title != "deploy do BFF" || !slices.Equal(got.MonitorIDs, []int64{b}) {
		t.Errorf("Get = %+v, want the new title and only monitor %d", got, b)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(fim) {
		t.Errorf("EndedAt = %v, want %s kept across the update", got.EndedAt, fim)
	}
}

func testMaintenanceEndKeepsTheEarliest(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	w := mustCreateMaintenance(t, s, newMaintenance([]int64{mustCreateMonitor(t, s, "a")}, nil))
	cedo, tarde := epoch.Add(5*time.Minute), epoch.Add(20*time.Minute)

	for _, at := range []time.Time{cedo, tarde} {
		if err := s.Maintenance().End(ctx, w.ID, at); err != nil {
			t.Fatalf("End(%s) returned unexpected error: %v", at, err)
		}
	}
	got, err := s.Maintenance().Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(cedo) {
		t.Errorf("EndedAt = %v, want the earliest end %s", got.EndedAt, cedo)
	}
}

func testMaintenanceRefusesMissingTargets(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	w := newMaintenance([]int64{999}, nil)
	if err := s.Maintenance().Create(ctx, &w); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Create with a missing monitor returned %v, want store.ErrNotFound", err)
	}
	w = newMaintenance(nil, []int64{999})
	if err := s.Maintenance().Create(ctx, &w); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Create with a missing group returned %v, want store.ErrNotFound", err)
	}
	todas, err := s.Maintenance().List(ctx)
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(todas) != 0 {
		t.Errorf("a refused Create left %d windows behind", len(todas))
	}
}

// Apagar o monitor ou o grupo tira o alvo: a janela passa a cobrir menos,
// nunca o que um dia reaproveite o número.
func testMaintenanceTargetsCascade(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	g := mustCreateGroup(t, s, "RPA", nil)
	m := mustCreateMonitor(t, s, "api")
	w := mustCreateMaintenance(t, s, newMaintenance([]int64{m}, []int64{g.ID}))

	if err := s.Monitors().Delete(ctx, m); err != nil {
		t.Fatalf("Monitors().Delete returned unexpected error: %v", err)
	}
	if err := s.Groups().Delete(ctx, g.ID); err != nil {
		t.Fatalf("Groups().Delete returned unexpected error: %v", err)
	}

	got, err := s.Maintenance().Get(ctx, w.ID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if len(got.MonitorIDs) != 0 || len(got.GroupIDs) != 0 {
		t.Errorf("targets = %v / %v, want both empty after the deletes", got.MonitorIDs, got.GroupIDs)
	}
}

func testMaintenanceDelete(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	w := mustCreateMaintenance(t, s, newMaintenance([]int64{mustCreateMonitor(t, s, "a")}, nil))
	if err := s.Maintenance().Delete(ctx, w.ID); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	if _, err := s.Maintenance().Get(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after Delete returned %v, want store.ErrNotFound", err)
	}
	if err := s.Maintenance().Delete(ctx, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Delete returned %v, want store.ErrNotFound", err)
	}
}
