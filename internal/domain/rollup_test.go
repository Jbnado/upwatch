package domain_test

import (
	"testing"

	"github.com/bernardojoao/upwatch/internal/domain"
)

func TestRollupUptimePercent(t *testing.T) {
	tests := []struct {
		name     string
		up       int
		degraded int
		down     int
		want     float64
	}{
		{"tudo no ar", 100, 0, 0, 100},
		{"tudo fora", 0, 0, 100, 0},
		{"noventa por cento", 90, 0, 10, 90},
		// Degradado ainda é serviço respondendo: conta como disponível
		// para efeito de SLA, senão latência alta viraria indisponibilidade.
		{"degradado conta como disponível", 80, 10, 10, 90},
		{"só degradado", 0, 50, 0, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := domain.Rollup{Up: tt.up, Degraded: tt.degraded, Down: tt.down}
			r.Total = tt.up + tt.degraded + tt.down

			if got := r.UptimePercent(); got != tt.want {
				t.Errorf("UptimePercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Amostra sem medição não pode contar contra o alvo. A rede do próprio
// host de monitoramento caindo, ou um monitor push que ainda não recebeu
// o primeiro sinal, produzem Unknown — e cobrar isso do alvo corromperia
// exatamente o número de SLA que se exporta para a gestão.
func TestRollupUptimePercentExcludesUnobservedSamples(t *testing.T) {
	tests := []struct {
		name    string
		up      int
		down    int
		unknown int
		want    float64
	}{
		{"desconhecido não conta como queda", 90, 10, 100, 90},
		{"só desconhecido não afirma nada", 0, 0, 50, 0},
		{"tudo no ar apesar do desconhecido", 50, 0, 50, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := domain.Rollup{Up: tt.up, Down: tt.down, Unknown: tt.unknown}
			r.Total = tt.up + tt.down + tt.unknown

			if got := r.UptimePercent(); got != tt.want {
				t.Errorf("UptimePercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Total conta tudo o que foi executado, inclusive o que não produziu
// medição: é a diferença entre "quantos checks rodaram" e "quantos
// observaram alguma coisa".
func TestRollupObservedCountsOnlyMeasuredSamples(t *testing.T) {
	r := domain.Rollup{Up: 40, Degraded: 5, Down: 5, Unknown: 50}
	r.Total = 100

	if got := r.Observed(); got != 50 {
		t.Errorf("Observed() = %d, want 50", got)
	}
	if r.Total != 100 {
		t.Errorf("Total = %d, want 100: every executed check counts", r.Total)
	}
}

// Bucket sem amostra nenhuma não pode dividir por zero nem reportar 100%
// de disponibilidade — não houve observação.
func TestRollupUptimePercentOnEmptyBucketIsZero(t *testing.T) {
	r := domain.Rollup{}

	if got := r.UptimePercent(); got != 0 {
		t.Errorf("UptimePercent() on empty rollup = %v, want 0", got)
	}
}

// Os contadores precisam suportar buckets diários de checks rápidos:
// 1 check por segundo em 24h dá 86.400 amostras, muito acima do limite
// de 32.767 de um smallint.
func TestRollupCountersHoldMoreThanSmallintRange(t *testing.T) {
	const oneCheckPerSecondForADay = 86400

	r := domain.Rollup{Up: oneCheckPerSecondForADay, Total: oneCheckPerSecondForADay}

	if r.Up != oneCheckPerSecondForADay {
		t.Errorf("Up = %d, want %d", r.Up, oneCheckPerSecondForADay)
	}
	if got := r.UptimePercent(); got != 100 {
		t.Errorf("UptimePercent() = %v, want 100", got)
	}
}
