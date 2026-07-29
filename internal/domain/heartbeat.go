package domain

import "time"

// DefaultProbeID identifica a própria instância do UpWatch como origem da
// batida. Probes remotos, quando existirem, gravam o próprio identificador
// e o histórico anterior continua válido sem reprocessamento.
const DefaultProbeID = "local"

// Heartbeat é o registro cru de um check.
//
// É a linha mais numerosa do banco: mantida por poucos dias e depois
// agregada em Rollup.
type Heartbeat struct {
	MonitorID int64     `json:"monitor_id"`
	ProbeID   string    `json:"probe_id"`
	Timestamp time.Time `json:"timestamp"`
	Status    Status    `json:"status"`

	// LatencyMS é o tempo de resposta em milissegundos. Só tem significado
	// quando o status é responsivo.
	LatencyMS int64 `json:"latency_ms"`

	// Message descreve a causa quando o check falhou.
	Message string `json:"message,omitempty"`
}

// Normalize aplica as invariantes de gravação: probe default, timestamp em
// UTC e latência zerada quando não houve resposta.
//
// Toda escrita passa por aqui para que o formato no banco seja o mesmo
// independentemente de quem produziu a batida.
func (h Heartbeat) Normalize() Heartbeat {
	if h.ProbeID == "" {
		h.ProbeID = DefaultProbeID
	}
	h.Timestamp = h.Timestamp.UTC()
	if !h.Status.Responsive() {
		h.LatencyMS = 0
	}
	return h
}
