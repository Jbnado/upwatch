package rollup_test

import (
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/rollup"
)

var bucketStart = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func hourlyBucket() rollup.Bucket {
	return rollup.Bucket{
		MonitorID:  1,
		ProbeID:    domain.DefaultProbeID,
		Resolution: domain.ResolutionHourly,
		Start:      bucketStart,
	}
}

// sample cria uma batida dentro do bucket.
func sample(offset time.Duration, status domain.Status, latency int64) domain.Heartbeat {
	return domain.Heartbeat{
		MonitorID: 1,
		ProbeID:   domain.DefaultProbeID,
		Timestamp: bucketStart.Add(offset),
		Status:    status,
		LatencyMS: latency,
	}
}

// repeat gera n batidas idênticas espaçadas em um minuto.
func repeat(n int, status domain.Status, latency int64) []domain.Heartbeat {
	out := make([]domain.Heartbeat, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, sample(time.Duration(i)*time.Second, status, latency))
	}
	return out
}

func TestAggregateCountsByStatus(t *testing.T) {
	var hbs []domain.Heartbeat
	hbs = append(hbs, repeat(80, domain.StatusUp, 100)...)
	hbs = append(hbs, repeat(10, domain.StatusDegraded, 900)...)
	hbs = append(hbs, repeat(10, domain.StatusDown, 0)...)

	got := rollup.Aggregate(hourlyBucket(), hbs)

	if got.Total != 100 {
		t.Errorf("Total = %d, want 100", got.Total)
	}
	if got.Up != 80 {
		t.Errorf("Up = %d, want 80", got.Up)
	}
	if got.Degraded != 10 {
		t.Errorf("Degraded = %d, want 10", got.Degraded)
	}
	if got.Down != 10 {
		t.Errorf("Down = %d, want 10", got.Down)
	}
}

func TestAggregateUptimePercent(t *testing.T) {
	tests := []struct {
		name string
		hbs  []domain.Heartbeat
		want float64
	}{
		{"tudo no ar", repeat(50, domain.StatusUp, 100), 100},
		{"tudo fora", repeat(50, domain.StatusDown, 0), 0},
		{
			"noventa por cento",
			append(repeat(90, domain.StatusUp, 100), repeat(10, domain.StatusDown, 0)...),
			90,
		},
		{
			// Degradado é serviço respondendo: conta como disponível, senão
			// latência alta viraria indisponibilidade no relatório de SLA.
			"degradado conta como disponível",
			append(repeat(50, domain.StatusDegraded, 900), repeat(50, domain.StatusUp, 100)...),
			100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rollup.Aggregate(hourlyBucket(), tt.hbs)
			if got.UptimePercent() != tt.want {
				t.Errorf("UptimePercent() = %v, want %v", got.UptimePercent(), tt.want)
			}
		})
	}
}

func TestAggregateCarriesBucketIdentity(t *testing.T) {
	b := rollup.Bucket{
		MonitorID: 42, ProbeID: "eu-west",
		Resolution: domain.ResolutionDaily, Start: bucketStart,
	}

	got := rollup.Aggregate(b, repeat(3, domain.StatusUp, 100))

	if got.MonitorID != 42 {
		t.Errorf("MonitorID = %d, want 42", got.MonitorID)
	}
	if got.ProbeID != "eu-west" {
		t.Errorf("ProbeID = %q, want %q", got.ProbeID, "eu-west")
	}
	if got.Resolution != domain.ResolutionDaily {
		t.Errorf("Resolution = %v, want %v", got.Resolution, domain.ResolutionDaily)
	}
	if !got.BucketStart.Equal(bucketStart) {
		t.Errorf("BucketStart = %v, want %v", got.BucketStart, bucketStart)
	}
}

// Percentil por posto mais próximo: sem interpolação, todo valor
// reportado corresponde a uma medição que de fato aconteceu.
func TestAggregatePercentilesOnKnownDataset(t *testing.T) {
	// Latências de 1 a 100 ms, uma por amostra.
	var hbs []domain.Heartbeat
	for i := 1; i <= 100; i++ {
		hbs = append(hbs, sample(time.Duration(i)*time.Second, domain.StatusUp, int64(i)))
	}

	got := rollup.Aggregate(hourlyBucket(), hbs)

	for _, tt := range []struct {
		name string
		got  float64
		want float64
	}{
		{"p50", got.LatencyP50MS, 50},
		{"p95", got.LatencyP95MS, 95},
		{"p99", got.LatencyP99MS, 99},
		{"min", got.LatencyMinMS, 1},
		{"max", got.LatencyMaxMS, 100},
		{"avg", got.LatencyAvgMS, 50.5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestAggregatePercentilesWithSingleSample(t *testing.T) {
	got := rollup.Aggregate(hourlyBucket(), []domain.Heartbeat{
		sample(0, domain.StatusUp, 42),
	})

	for _, tt := range []struct {
		name string
		got  float64
	}{
		{"p50", got.LatencyP50MS},
		{"p95", got.LatencyP95MS},
		{"p99", got.LatencyP99MS},
		{"min", got.LatencyMinMS},
		{"max", got.LatencyMaxMS},
		{"avg", got.LatencyAvgMS},
	} {
		if tt.got != 42 {
			t.Errorf("%s = %v, want 42", tt.name, tt.got)
		}
	}
}

func TestAggregatePercentilesWithTwoSamples(t *testing.T) {
	got := rollup.Aggregate(hourlyBucket(), []domain.Heartbeat{
		sample(0, domain.StatusUp, 10),
		sample(time.Second, domain.StatusUp, 20),
	})

	if got.LatencyP50MS != 10 {
		t.Errorf("p50 = %v, want 10", got.LatencyP50MS)
	}
	if got.LatencyP95MS != 20 {
		t.Errorf("p95 = %v, want 20", got.LatencyP95MS)
	}
	if got.LatencyP99MS != 20 {
		t.Errorf("p99 = %v, want 20", got.LatencyP99MS)
	}
}

// A ordem de chegada não pode influenciar o percentil.
func TestAggregatePercentilesIgnoreInputOrder(t *testing.T) {
	ascending := []domain.Heartbeat{
		sample(0, domain.StatusUp, 10),
		sample(time.Second, domain.StatusUp, 50),
		sample(2*time.Second, domain.StatusUp, 90),
	}
	shuffled := []domain.Heartbeat{
		sample(0, domain.StatusUp, 90),
		sample(time.Second, domain.StatusUp, 10),
		sample(2*time.Second, domain.StatusUp, 50),
	}

	a := rollup.Aggregate(hourlyBucket(), ascending)
	b := rollup.Aggregate(hourlyBucket(), shuffled)

	if a.LatencyP50MS != b.LatencyP50MS || a.LatencyP95MS != b.LatencyP95MS {
		t.Errorf("percentiles differ by input order: %v vs %v",
			[]float64{a.LatencyP50MS, a.LatencyP95MS},
			[]float64{b.LatencyP50MS, b.LatencyP95MS})
	}
}

// Latência de batida sem resposta é ruído: incluí-la puxaria os percentis
// para baixo e faria uma queda total parecer melhoria de desempenho.
func TestAggregateExcludesDownSamplesFromLatency(t *testing.T) {
	hbs := []domain.Heartbeat{
		sample(0, domain.StatusUp, 100),
		sample(time.Second, domain.StatusUp, 200),
		sample(2*time.Second, domain.StatusDown, 0),
		sample(3*time.Second, domain.StatusDown, 0),
	}

	got := rollup.Aggregate(hourlyBucket(), hbs)

	if got.LatencySamples != 2 {
		t.Errorf("LatencySamples = %d, want 2", got.LatencySamples)
	}
	if got.LatencyAvgMS != 150 {
		t.Errorf("LatencyAvgMS = %v, want 150", got.LatencyAvgMS)
	}
	if got.LatencyMinMS != 100 {
		t.Errorf("LatencyMinMS = %v, want 100", got.LatencyMinMS)
	}
}

// Degradado respondeu, apenas devagar: é exatamente a amostra que os
// percentis de cauda precisam enxergar.
func TestAggregateIncludesDegradedSamplesInLatency(t *testing.T) {
	hbs := []domain.Heartbeat{
		sample(0, domain.StatusUp, 100),
		sample(time.Second, domain.StatusDegraded, 900),
	}

	got := rollup.Aggregate(hourlyBucket(), hbs)

	if got.LatencySamples != 2 {
		t.Errorf("LatencySamples = %d, want 2", got.LatencySamples)
	}
	if got.LatencyMaxMS != 900 {
		t.Errorf("LatencyMaxMS = %v, want 900", got.LatencyMaxMS)
	}
}

// Bucket em que o serviço só esteve fora não tem latência a reportar;
// zerar é honesto, inventar um número não.
func TestAggregateWithNoResponsiveSamplesHasZeroLatency(t *testing.T) {
	got := rollup.Aggregate(hourlyBucket(), repeat(10, domain.StatusDown, 0))

	if got.LatencySamples != 0 {
		t.Errorf("LatencySamples = %d, want 0", got.LatencySamples)
	}
	for _, v := range []float64{
		got.LatencyAvgMS, got.LatencyMinMS, got.LatencyMaxMS,
		got.LatencyP50MS, got.LatencyP95MS, got.LatencyP99MS,
	} {
		if v != 0 {
			t.Errorf("latency statistic = %v, want 0 when nothing responded", v)
		}
	}
	if got.Total != 10 || got.Down != 10 {
		t.Errorf("counters = (total %d, down %d), want (10, 10)", got.Total, got.Down)
	}
}

// Amostra fora da janela pertence a outro bucket; contá-la duas vezes
// distorceria os dois.
func TestAggregateIgnoresSamplesOutsideTheBucket(t *testing.T) {
	hbs := []domain.Heartbeat{
		sample(-time.Second, domain.StatusDown, 0),   // bucket anterior
		sample(0, domain.StatusUp, 100),              // dentro, início inclusivo
		sample(30*time.Minute, domain.StatusUp, 100), // dentro
		sample(time.Hour, domain.StatusDown, 0),      // fim exclusivo: próximo bucket
		sample(2*time.Hour, domain.StatusDown, 0),    // muito depois
	}

	got := rollup.Aggregate(hourlyBucket(), hbs)

	if got.Total != 2 {
		t.Errorf("Total = %d, want 2: only samples inside the bucket count", got.Total)
	}
	if got.Down != 0 {
		t.Errorf("Down = %d, want 0", got.Down)
	}
}

// Buckets diários cobrem 24 horas; usar a janela da resolução errada
// descartaria quase tudo.
func TestAggregateUsesResolutionWindow(t *testing.T) {
	b := hourlyBucket()
	b.Resolution = domain.ResolutionDaily

	hbs := []domain.Heartbeat{
		sample(0, domain.StatusUp, 100),
		sample(5*time.Hour, domain.StatusUp, 100),
		sample(23*time.Hour, domain.StatusUp, 100),
		sample(24*time.Hour, domain.StatusUp, 100), // fora: próximo dia
	}

	got := rollup.Aggregate(b, hbs)

	if got.Total != 3 {
		t.Errorf("Total = %d, want 3", got.Total)
	}
}

func TestAggregateOnEmptyInputProducesEmptyRollup(t *testing.T) {
	got := rollup.Aggregate(hourlyBucket(), nil)

	if got.Total != 0 {
		t.Errorf("Total = %d, want 0", got.Total)
	}
	if got.UptimePercent() != 0 {
		t.Errorf("UptimePercent() = %v, want 0: no observation means no uptime to claim",
			got.UptimePercent())
	}
}

// Um bucket diário com um check por segundo passa de 86 mil amostras,
// muito além do alcance de smallint — a coluna que o Uptime Kuma usa.
func TestAggregateHandlesVolumeBeyondSmallintRange(t *testing.T) {
	const n = 86_400
	b := hourlyBucket()
	b.Resolution = domain.ResolutionDaily

	hbs := make([]domain.Heartbeat, 0, n)
	for i := 0; i < n; i++ {
		hbs = append(hbs, sample(time.Duration(i)*time.Second, domain.StatusUp, 100))
	}

	got := rollup.Aggregate(b, hbs)

	if got.Total != n {
		t.Errorf("Total = %d, want %d", got.Total, n)
	}
	if got.Up != n {
		t.Errorf("Up = %d, want %d", got.Up, n)
	}
	if got.UptimePercent() != 100 {
		t.Errorf("UptimePercent() = %v, want 100", got.UptimePercent())
	}
}

// Aggregate não pode alterar a fatia recebida: o worker reaproveita o
// mesmo lote para produzir o bucket horário e o diário.
func TestAggregateDoesNotMutateInput(t *testing.T) {
	hbs := []domain.Heartbeat{
		sample(0, domain.StatusUp, 300),
		sample(time.Second, domain.StatusUp, 100),
		sample(2*time.Second, domain.StatusUp, 200),
	}
	before := []int64{hbs[0].LatencyMS, hbs[1].LatencyMS, hbs[2].LatencyMS}

	rollup.Aggregate(hourlyBucket(), hbs)

	for i, want := range before {
		if hbs[i].LatencyMS != want {
			t.Errorf("input heartbeat %d changed: LatencyMS = %d, want %d", i, hbs[i].LatencyMS, want)
		}
	}
}

// Duas resoluções sobre o mesmo lote precisam produzir percentis exatos
// cada uma. Derivar o diário a partir do horário produziria percentil de
// percentis, que não corresponde a nenhuma medição real.
func TestAggregateGivesExactPercentilesPerResolution(t *testing.T) {
	var hbs []domain.Heartbeat
	for i := 1; i <= 100; i++ {
		hbs = append(hbs, sample(time.Duration(i)*time.Minute, domain.StatusUp, int64(i)))
	}

	hourly := hourlyBucket()
	daily := hourlyBucket()
	daily.Resolution = domain.ResolutionDaily

	gotHourly := rollup.Aggregate(hourly, hbs)
	gotDaily := rollup.Aggregate(daily, hbs)

	// A primeira hora cobre as amostras de 1 a 59 minutos.
	if gotHourly.Total != 59 {
		t.Errorf("hourly Total = %d, want 59", gotHourly.Total)
	}
	if gotHourly.LatencyP95MS != 57 {
		t.Errorf("hourly p95 = %v, want 57", gotHourly.LatencyP95MS)
	}
	// O dia cobre as 100.
	if gotDaily.Total != 100 {
		t.Errorf("daily Total = %d, want 100", gotDaily.Total)
	}
	if gotDaily.LatencyP95MS != 95 {
		t.Errorf("daily p95 = %v, want 95", gotDaily.LatencyP95MS)
	}
}
