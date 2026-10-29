package maintenance_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/maintenance"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

var epoch = time.Date(2026, 10, 29, 22, 0, 0, 0, time.UTC)

// O calendário responde do que leu na última releitura: a janela gravada
// depois só vale quando alguém avisa (a API, a cada escrita) ou quando o
// relógio da releitura passa.
func TestCalendarAnswersFromTheLastReload(t *testing.T) {
	ctx := context.Background()
	st, err := sqlstore.OpenSQLite(filepath.Join(t.TempDir(), "upwatch.db"), secretbox.Ephemeral())
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	rpa := domain.Group{Name: "RPA", Slug: "rpa"}
	if err := st.Groups().Create(ctx, &rpa); err != nil {
		t.Fatalf("Groups().Create returned unexpected error: %v", err)
	}
	prod := domain.Group{Name: "prod", Slug: "prod", ParentID: &rpa.ID}
	if err := st.Groups().Create(ctx, &prod); err != nil {
		t.Fatalf("Groups().Create returned unexpected error: %v", err)
	}
	m := domain.Monitor{ID: 7, GroupID: &prod.ID}
	nenhum := func(int64) (domain.Monitor, bool) { return domain.Monitor{}, false }

	cal := maintenance.New(st)
	if err := cal.Reload(ctx); err != nil {
		t.Fatalf("Reload returned unexpected error: %v", err)
	}

	w := domain.MaintenanceWindow{
		Title: "deploy", StartsAt: epoch, EndsAt: epoch.Add(30 * time.Minute),
		Recurrence: domain.RecurrenceNone, TimeZone: "UTC", GroupIDs: []int64{rpa.ID},
	}
	if err := st.Maintenance().Create(ctx, &w); err != nil {
		t.Fatalf("Maintenance().Create returned unexpected error: %v", err)
	}
	if _, ok := cal.Covering(m, nenhum, epoch.Add(time.Minute)); ok {
		t.Fatal("the calendar answered from the database without a reload")
	}

	if err := cal.Reload(ctx); err != nil {
		t.Fatalf("Reload returned unexpected error: %v", err)
	}
	got, ok := cal.Covering(m, nenhum, epoch.Add(time.Minute))
	if !ok || got.ID != w.ID {
		t.Errorf("Covering after reload = %+v, %v; want the window on RPA covering RPA/prod", got, ok)
	}
}
