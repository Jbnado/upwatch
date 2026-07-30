package store_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// Limite ausente não pode virar "traga tudo": uma listagem sem teto é
// exatamente o que degrada quando a tabela cresce.
func TestPageFilterNormalizeAppliesDefaultLimit(t *testing.T) {
	f := store.PageFilter{}

	f = f.Normalize()

	if f.Limit != store.DefaultPageSize {
		t.Errorf("Limit = %d, want %d", f.Limit, store.DefaultPageSize)
	}
}

func TestPageFilterNormalizeClampsLimitToMaximum(t *testing.T) {
	f := store.PageFilter{Limit: store.MaxPageSize + 1000}

	f = f.Normalize()

	if f.Limit != store.MaxPageSize {
		t.Errorf("Limit = %d, want %d", f.Limit, store.MaxPageSize)
	}
}

func TestPageFilterNormalizeRejectsNegativeLimit(t *testing.T) {
	f := store.PageFilter{Limit: -5}

	f = f.Normalize()

	if f.Limit != store.DefaultPageSize {
		t.Errorf("Limit = %d, want %d", f.Limit, store.DefaultPageSize)
	}
}

func TestPageFilterNormalizeKeepsValidLimit(t *testing.T) {
	f := store.PageFilter{Limit: 25}

	f = f.Normalize()

	if f.Limit != 25 {
		t.Errorf("Limit = %d, want 25", f.Limit)
	}
}

// A janela é sempre normalizada para UTC porque os timestamps gravados
// são UTC; comparar contra um horário local desloca o intervalo.
func TestTimeRangeNormalizeConvertsToUTC(t *testing.T) {
	saoPaulo := time.FixedZone("America/Sao_Paulo", -3*60*60)
	r := store.TimeRange{
		From: time.Date(2026, 7, 28, 0, 0, 0, 0, saoPaulo),
		To:   time.Date(2026, 7, 29, 0, 0, 0, 0, saoPaulo),
	}

	r = r.Normalize()

	if r.From.Location() != time.UTC || r.To.Location() != time.UTC {
		t.Errorf("locations = (%v, %v), want UTC", r.From.Location(), r.To.Location())
	}
}

func TestTimeRangeContains(t *testing.T) {
	r := store.TimeRange{
		From: time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name string
		ts   time.Time
		want bool
	}{
		{"antes do início", time.Date(2026, 7, 27, 23, 59, 59, 0, time.UTC), false},
		{"no início (inclusivo)", r.From, true},
		{"no meio", time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC), true},
		{"no fim (exclusivo)", r.To, false},
		{"depois do fim", time.Date(2026, 7, 29, 0, 0, 1, 0, time.UTC), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.Contains(tt.ts); got != tt.want {
				t.Errorf("Contains(%v) = %v, want %v", tt.ts, got, tt.want)
			}
		})
	}
}

func TestTimeRangeValidRejectsInvertedWindow(t *testing.T) {
	r := store.TimeRange{
		From: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC),
	}

	if r.Valid() {
		t.Error("Valid() = true for an inverted window, want false")
	}
}

func TestHeartbeatQueryNormalizeAppliesDefaults(t *testing.T) {
	q := store.HeartbeatQuery{MonitorID: 1}

	q = q.Normalize()

	if q.Limit != store.DefaultPageSize {
		t.Errorf("Limit = %d, want %d", q.Limit, store.DefaultPageSize)
	}
}

func TestRollupQueryNormalizeDefaultsToHourly(t *testing.T) {
	q := store.RollupQuery{MonitorID: 1}

	q = q.Normalize()

	if q.Resolution != domain.ResolutionHourly {
		t.Errorf("Resolution = %v, want %v", q.Resolution, domain.ResolutionHourly)
	}
}
