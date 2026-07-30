// Package metrics guarda os contadores acumulados do processo.
//
// Existe porque contador de Prometheus precisa ser barato de ler: a
// raspagem acontece a cada poucos segundos, e derivar "quantas
// verificações houve" de uma varredura na tabela de batidas custaria uma
// leitura completa por coleta — a métrica passaria a ser a maior fonte
// de carga do banco que ela deveria observar.
//
// Zerar no reinício é aceitável e esperado: o Prometheus reconhece a
// queda de um contador como reinício e não a confunde com regressão.
package metrics

import (
	"sync/atomic"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Counters acumula o que o processo viu desde que subiu.
//
// Seguro para uso concorrente: o agendador escreve de várias goroutines
// enquanto a raspagem lê.
type Counters struct {
	up       atomic.Int64
	down     atomic.Int64
	degraded atomic.Int64
	unknown  atomic.Int64

	notificationsSent    atomic.Int64
	notificationsDropped atomic.Int64
}

// New devolve um conjunto zerado.
func New() *Counters { return &Counters{} }

// ObserveCheck registra uma verificação concluída.
func (c *Counters) ObserveCheck(s domain.Status) {
	if c == nil {
		return
	}
	switch s {
	case domain.StatusUp:
		c.up.Add(1)
	case domain.StatusDown:
		c.down.Add(1)
	case domain.StatusDegraded:
		c.degraded.Add(1)
	default:
		c.unknown.Add(1)
	}
}

// Checks devolve a contagem de um estado.
func (c *Counters) Checks(s domain.Status) int64 {
	if c == nil {
		return 0
	}
	switch s {
	case domain.StatusUp:
		return c.up.Load()
	case domain.StatusDown:
		return c.down.Load()
	case domain.StatusDegraded:
		return c.degraded.Load()
	default:
		return c.unknown.Load()
	}
}

// ObserveNotification registra uma tentativa de aviso.
//
// Entregue e descartado contados à parte: durante um incidente a
// pergunta que importa é se o aviso saiu, e um número só não responde.
func (c *Counters) ObserveNotification(entregue bool) {
	if c == nil {
		return
	}
	if entregue {
		c.notificationsSent.Add(1)
		return
	}
	c.notificationsDropped.Add(1)
}

// NotificationsSent é quantos avisos saíram.
func (c *Counters) NotificationsSent() int64 {
	if c == nil {
		return 0
	}
	return c.notificationsSent.Load()
}

// NotificationsDropped é quantos avisos não saíram.
func (c *Counters) NotificationsDropped() int64 {
	if c == nil {
		return 0
	}
	return c.notificationsDropped.Load()
}
