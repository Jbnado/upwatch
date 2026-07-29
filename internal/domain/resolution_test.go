package domain_test

import (
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

func TestResolutionString(t *testing.T) {
	tests := []struct {
		res  domain.Resolution
		want string
	}{
		{domain.ResolutionHourly, "hourly"},
		{domain.ResolutionDaily, "daily"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.res.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseResolutionAcceptsKnownNames(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want domain.Resolution
	}{
		{"hourly", domain.ResolutionHourly},
		{"daily", domain.ResolutionDaily},
	} {
		t.Run(tt.in, func(t *testing.T) {
			got, err := domain.ParseResolution(tt.in)
			if err != nil {
				t.Fatalf("ParseResolution(%q) returned unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseResolution(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseResolutionRejectsUnknownName(t *testing.T) {
	if _, err := domain.ParseResolution("weekly"); err == nil {
		t.Fatal("ParseResolution(\"weekly\") returned nil error, want an error")
	}
}

func TestResolutionDuration(t *testing.T) {
	if got := domain.ResolutionHourly.Duration(); got != time.Hour {
		t.Errorf("ResolutionHourly.Duration() = %v, want %v", got, time.Hour)
	}
	if got := domain.ResolutionDaily.Duration(); got != 24*time.Hour {
		t.Errorf("ResolutionDaily.Duration() = %v, want %v", got, 24*time.Hour)
	}
}

func TestResolutionTruncateToHourDropsMinutesAndBelow(t *testing.T) {
	in := time.Date(2026, 7, 28, 14, 37, 22, 123456789, time.UTC)
	want := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)

	got := domain.ResolutionHourly.Truncate(in)

	if !got.Equal(want) {
		t.Errorf("Truncate(%v) = %v, want %v", in, got, want)
	}
}

func TestResolutionTruncateToDayDropsHoursAndBelow(t *testing.T) {
	in := time.Date(2026, 7, 28, 14, 37, 22, 123456789, time.UTC)
	want := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

	got := domain.ResolutionDaily.Truncate(in)

	if !got.Equal(want) {
		t.Errorf("Truncate(%v) = %v, want %v", in, got, want)
	}
}

// Buckets são sempre UTC. Um horário em fuso negativo que já cruzou a
// meia-noite UTC pertence ao dia UTC seguinte — truncar no fuso local
// colocaria a amostra no bucket errado.
func TestResolutionTruncateNormalizesToUTC(t *testing.T) {
	saoPaulo := time.FixedZone("America/Sao_Paulo", -3*60*60)
	// 2026-07-28 22:30 -03:00 == 2026-07-29 01:30 UTC
	in := time.Date(2026, 7, 28, 22, 30, 0, 0, saoPaulo)
	want := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)

	got := domain.ResolutionDaily.Truncate(in)

	if !got.Equal(want) {
		t.Errorf("Truncate(%v) = %v, want %v", in, got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("Truncate returned location %v, want UTC", got.Location())
	}
}

func TestResolutionTruncateIsIdempotent(t *testing.T) {
	in := time.Date(2026, 7, 28, 14, 37, 22, 0, time.UTC)

	for _, res := range []domain.Resolution{domain.ResolutionHourly, domain.ResolutionDaily} {
		t.Run(res.String(), func(t *testing.T) {
			once := res.Truncate(in)
			twice := res.Truncate(once)
			if !once.Equal(twice) {
				t.Errorf("Truncate not idempotent: once=%v twice=%v", once, twice)
			}
		})
	}
}

// O worker de rollup só pode processar buckets já encerrados; agregar o
// bucket corrente produziria estatística parcial gravada como definitiva.
func TestResolutionBucketIsClosedOnlyAfterItEnds(t *testing.T) {
	bucket := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"durante o bucket", time.Date(2026, 7, 28, 14, 30, 0, 0, time.UTC), false},
		{"exatamente no fim", time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC), true},
		{"depois do fim", time.Date(2026, 7, 28, 15, 0, 1, 0, time.UTC), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.ResolutionHourly.BucketClosed(bucket, tt.now)
			if got != tt.want {
				t.Errorf("BucketClosed(%v, %v) = %v, want %v", bucket, tt.now, got, tt.want)
			}
		})
	}
}
