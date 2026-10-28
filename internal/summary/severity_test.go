package summary_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/summary"
)

// A faixa do painel e o agregado escolhem o pior estado pela mesma ordem
// do resto do sistema, com "sem medição" como ausência: o bucket sem
// batida não esconde a batida que veio.
func TestBucketsUseTheDomainOrder(t *testing.T) {
	inicio := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	todos := []domain.Status{domain.StatusUnknown, domain.StatusUp, domain.StatusDegraded, domain.StatusDown}

	for _, a := range todos {
		for _, b := range todos {
			want := domain.Worst([]domain.Status{a, b}, domain.UnknownIsAbsence)

			serie := summary.Series(inicio, inicio.Add(time.Minute), 1, []domain.Heartbeat{
				{Timestamp: inicio, Status: a}, {Timestamp: inicio.Add(time.Second), Status: b},
			})
			if serie[0].Status != want {
				t.Errorf("Series com %v e %v = %v, want %v", a, b, serie[0].Status, want)
			}

			var r domain.Rollup
			for _, s := range []domain.Status{a, b} {
				switch s {
				case domain.StatusUp:
					r.Up++
				case domain.StatusDegraded:
					r.Degraded++
				case domain.StatusDown:
					r.Down++
				default:
					r.Unknown++
				}
			}
			if got := summary.BucketStatus(r); got != want {
				t.Errorf("BucketStatus com %v e %v = %v, want %v", a, b, got, want)
			}
		}
	}
}
