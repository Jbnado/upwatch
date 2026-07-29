package domain

import "time"

// Rollup é a estatística agregada de um monitor num bucket de tempo.
//
// É o que permite guardar meses de histórico sem guardar meses de batidas
// cruas. Os contadores são int — um bucket diário com check por segundo
// chega a 86.400 amostras, muito acima do alcance de um smallint.
type Rollup struct {
	MonitorID   int64      `json:"monitor_id"`
	ProbeID     string     `json:"probe_id"`
	Resolution  Resolution `json:"resolution"`
	BucketStart time.Time  `json:"bucket_start"`

	Total    int `json:"total"`
	Up       int `json:"up"`
	Down     int `json:"down"`
	Degraded int `json:"degraded"`

	// Estatísticas de latência, calculadas apenas sobre amostras
	// responsivas. Percentis são exatos: derivamos horário e diário
	// direto das batidas cruas, nunca de percentis parciais.
	LatencySamples int     `json:"latency_samples"`
	LatencyAvgMS   float64 `json:"latency_avg_ms"`
	LatencyMinMS   float64 `json:"latency_min_ms"`
	LatencyMaxMS   float64 `json:"latency_max_ms"`
	LatencyP50MS   float64 `json:"latency_p50_ms"`
	LatencyP95MS   float64 `json:"latency_p95_ms"`
	LatencyP99MS   float64 `json:"latency_p99_ms"`
}

// UptimePercent é a fração de amostras em que o serviço respondeu.
//
// Degradado conta como disponível: o serviço respondeu, apenas devagar.
// Bucket sem amostra devolve 0 — não houve observação, e reportar 100%
// inventaria disponibilidade que ninguém mediu.
func (r Rollup) UptimePercent() float64 {
	if r.Total <= 0 {
		return 0
	}
	return float64(r.Total-r.Down) / float64(r.Total) * 100
}
