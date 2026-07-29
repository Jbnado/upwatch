// Package rollup agrega batidas cruas em estatísticas por período.
//
// É o que permite guardar meses de histórico sem guardar meses de batidas:
// o dado cru vive poucos dias e os agregados sobrevivem por muito mais,
// com o banco estabilizando num tamanho em vez de crescer sem limite.
package rollup

import (
	"math"
	"sort"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// Bucket identifica a janela agregada.
type Bucket struct {
	MonitorID  int64
	ProbeID    string
	Resolution domain.Resolution
	Start      time.Time
}

// End é o fim exclusivo da janela.
func (b Bucket) End() time.Time { return b.Start.Add(b.Resolution.Duration()) }

// Contains testa se a batida pertence ao bucket, com início inclusivo e
// fim exclusivo — sem isso a amostra da fronteira entraria em dois buckets.
func (b Bucket) Contains(ts time.Time) bool {
	return !ts.Before(b.Start) && ts.Before(b.End())
}

// Aggregate resume as batidas do bucket numa estatística.
//
// É uma função pura: nenhum I/O, nenhuma dependência de relógio, nenhum
// SQL. Isso a torna verificável com tabelas de casos em microssegundos, e
// é o que permite que a mesma agregação sirva SQLite, PostgreSQL e
// qualquer backend futuro sem uma linha de código específica de dialeto.
func Aggregate(b Bucket, hbs []domain.Heartbeat) domain.Rollup {
	out := domain.Rollup{
		MonitorID:   b.MonitorID,
		ProbeID:     b.ProbeID,
		Resolution:  b.Resolution,
		BucketStart: b.Start,
	}

	// Latências são copiadas para ordenação; ordenar a fatia recebida
	// alteraria a entrada, e o worker reaproveita o mesmo lote para
	// produzir os buckets horário e diário.
	var latencies []float64

	for _, hb := range hbs {
		if !b.Contains(hb.Timestamp) {
			continue
		}

		out.Total++
		switch hb.Status {
		case domain.StatusUp:
			out.Up++
		case domain.StatusDegraded:
			out.Degraded++
		default:
			out.Down++
		}

		// Só amostra que obteve resposta entra na latência: incluir uma
		// falha puxaria os percentis para baixo e faria uma queda total
		// parecer melhoria de desempenho.
		if hb.Status.Responsive() {
			latencies = append(latencies, float64(hb.LatencyMS))
		}
	}

	if len(latencies) == 0 {
		return out
	}

	sort.Float64s(latencies)

	out.LatencySamples = len(latencies)
	out.LatencyMinMS = latencies[0]
	out.LatencyMaxMS = latencies[len(latencies)-1]
	out.LatencyAvgMS = mean(latencies)
	out.LatencyP50MS = percentile(latencies, 50)
	out.LatencyP95MS = percentile(latencies, 95)
	out.LatencyP99MS = percentile(latencies, 99)

	return out
}

func mean(sorted []float64) float64 {
	var sum float64
	for _, v := range sorted {
		sum += v
	}
	return sum / float64(len(sorted))
}

// percentile usa o método do posto mais próximo sobre a amostra já
// ordenada.
//
// Sem interpolação de propósito: todo valor reportado corresponde a uma
// latência que de fato foi medida, em vez de a um ponto sintético entre
// duas medições. Para p95, o posto é ceil(0,95 × n).
func percentile(sorted []float64, p float64) float64 {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
