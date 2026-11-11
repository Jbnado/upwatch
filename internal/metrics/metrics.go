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
	"sync"
	"sync/atomic"
	"time"

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

	schedulerMu       sync.Mutex
	schedulerWaiting  int64
	schedulerRunning  int64
	schedulerDelay    schedulerHistogram
	schedulerDuration schedulerHistogram
}

// SchedulerBucket é uma faixa cumulativa de um histograma Prometheus.
type SchedulerBucket struct {
	UpperBound string
	Count      int64
}

// SchedulerHistogram é uma fotografia das amostras em segundos.
type SchedulerHistogram struct {
	Buckets    [8]SchedulerBucket
	Count      int64
	SumSeconds float64
}

// SchedulerSnapshot reúne a fila atual e as observações do agendador.
type SchedulerSnapshot struct {
	Waiting       int64
	Running       int64
	StartDelay    SchedulerHistogram
	CheckDuration SchedulerHistogram
}

var schedulerBounds = [...]time.Duration{
	100 * time.Millisecond, 500 * time.Millisecond, time.Second,
	5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute,
}

var schedulerLabels = [...]string{"0.1", "0.5", "1", "5", "10", "30", "60", "+Inf"}

type schedulerHistogram struct {
	buckets    [8]int64
	count      int64
	sumSeconds float64
}

func (h *schedulerHistogram) observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.count++
	h.sumSeconds += d.Seconds()
	for i, bound := range schedulerBounds {
		if d <= bound {
			h.buckets[i]++
			return
		}
	}
	h.buckets[len(h.buckets)-1]++
}

func (h schedulerHistogram) snapshot() SchedulerHistogram {
	result := SchedulerHistogram{Count: h.count, SumSeconds: h.sumSeconds}
	var cumulative int64
	for i, n := range h.buckets {
		cumulative += n
		result.Buckets[i] = SchedulerBucket{UpperBound: schedulerLabels[i], Count: cumulative}
	}
	return result
}

// QueueSchedulerChecks inclui checks vencidos que aguardam um worker.
func (c *Counters) QueueSchedulerChecks(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.schedulerMu.Lock()
	c.schedulerWaiting += int64(n)
	c.schedulerMu.Unlock()
}

// UnqueueSchedulerChecks retira da espera checks cancelados ou removidos.
func (c *Counters) UnqueueSchedulerChecks(n int) {
	if c == nil || n <= 0 {
		return
	}
	c.schedulerMu.Lock()
	c.schedulerWaiting -= min(int64(n), c.schedulerWaiting)
	c.schedulerMu.Unlock()
}

// StartSchedulerCheck registra o atraso e move um check da espera à execução.
func (c *Counters) StartSchedulerCheck(delay time.Duration) {
	if c == nil {
		return
	}
	c.schedulerMu.Lock()
	if c.schedulerWaiting > 0 {
		c.schedulerWaiting--
	}
	c.schedulerRunning++
	c.schedulerDelay.observe(delay)
	c.schedulerMu.Unlock()
}

// FinishSchedulerCheck registra o tempo do checker e encerra sua execução.
func (c *Counters) FinishSchedulerCheck(duration time.Duration) {
	if c == nil {
		return
	}
	c.schedulerMu.Lock()
	if c.schedulerRunning > 0 {
		c.schedulerRunning--
	}
	c.schedulerDuration.observe(duration)
	c.schedulerMu.Unlock()
}

// SchedulerSnapshot devolve uma fotografia coerente para a raspagem.
func (c *Counters) SchedulerSnapshot() SchedulerSnapshot {
	if c == nil {
		empty := schedulerHistogram{}.snapshot()
		return SchedulerSnapshot{StartDelay: empty, CheckDuration: empty}
	}
	c.schedulerMu.Lock()
	defer c.schedulerMu.Unlock()
	return SchedulerSnapshot{
		Waiting:       c.schedulerWaiting,
		Running:       c.schedulerRunning,
		StartDelay:    c.schedulerDelay.snapshot(),
		CheckDuration: c.schedulerDuration.snapshot(),
	}
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
