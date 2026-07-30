package notifier

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/metrics"
)

// Padrões do despachante.
const (
	// DefaultMaxAttempts é quantas vezes uma entrega é tentada.
	//
	// Falha momentânea é o caso comum, então desistir na primeira
	// perderia avisos por um soluço de rede; retentar para sempre
	// transformaria um destino removido num laço infinito.
	DefaultMaxAttempts = 4

	// DefaultRetryDelay é a espera antes da primeira retentativa. Cresce a
	// cada tentativa.
	DefaultRetryDelay = 2 * time.Second

	// DefaultQueueSize dá folga para uma rajada de incidentes.
	DefaultQueueSize = 256

	// DefaultWorkers entrega em paralelo, para um canal lento não segurar
	// os demais.
	DefaultWorkers = 4
)

// DispatcherOptions configura o despachante.
type DispatcherOptions struct {
	MaxAttempts int
	RetryDelay  time.Duration
	QueueSize   int
	Workers     int
	Clock       clock.Clock

	// Counters separa entregue de descartado na exposição de métricas.
	//
	// Opcional: o contador nulo aceita observações sem fazer nada, então
	// quem monta um despachante em teste não precisa saber que ele
	// existe.
	Counters *metrics.Counters
}

func (o DispatcherOptions) withDefaults() DispatcherOptions {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.RetryDelay <= 0 {
		o.RetryDelay = DefaultRetryDelay
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	return o
}

// delivery é um aviso destinado a um canal.
type delivery struct {
	notification Notification
	channel      Notifier
}

// Dispatcher entrega avisos fora do caminho crítico do monitoramento.
//
// A separação é o ponto do componente: um canal fora do ar não pode
// atrasar o registro das batidas, e uma tempestade de incidentes não pode
// travar o agendador.
type Dispatcher struct {
	opts DispatcherOptions

	queue   chan delivery
	dropped atomic.Int64
}

// NewDispatcher cria o despachante. É preciso chamar Run para ele operar.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	opts = opts.withDefaults()

	return &Dispatcher{
		opts:  opts,
		queue: make(chan delivery, opts.QueueSize),
	}
}

// MaxAttempts é o teto de tentativas por entrega.
func (d *Dispatcher) MaxAttempts() int { return d.opts.MaxAttempts }

// Dropped conta os avisos descartados por fila cheia ou tentativas
// esgotadas.
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// Enqueue aceita um aviso para entrega.
//
// Nunca bloqueia. Quem chama está no caminho crítico do monitoramento, e
// prendê-lo atrás de um canal lento faria o agendador parar de verificar
// alvos — perder um aviso é ruim, parar de monitorar é pior. O descarte é
// contado para não acontecer em silêncio.
func (d *Dispatcher) Enqueue(n Notification, channels []Notifier) {
	for _, canal := range channels {
		if canal == nil {
			continue
		}

		select {
		case d.queue <- delivery{notification: n, channel: canal}:
		default:
			d.dropped.Add(1)
			d.opts.Counters.ObserveNotification(false)
			slog.Warn("aviso descartado: fila de notificacao cheia",
				"monitor", n.Monitor.Name, "estado", n.Status())
		}
	}
}

// Run entrega os avisos até o contexto terminar.
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup

	for i := 0; i < d.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.work(ctx)
		}()
	}

	wg.Wait()

	// O que já foi aceito precisa sair antes de o processo encerrar:
	// perdê-lo apagaria justamente a notícia da queda.
	d.drain()
}

func (d *Dispatcher) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-d.queue:
			d.deliver(ctx, item)
		}
	}
}

// drain entrega o que restou na fila, sem o contexto já cancelado.
func (d *Dispatcher) drain() {
	ctx := context.Background()

	for {
		select {
		case item := <-d.queue:
			d.deliver(ctx, item)
		default:
			return
		}
	}
}

// deliver tenta a entrega com retentativas espaçadas.
func (d *Dispatcher) deliver(ctx context.Context, item delivery) {
	espera := d.opts.RetryDelay

	for tentativa := 1; tentativa <= d.opts.MaxAttempts; tentativa++ {
		err := item.channel.Send(ctx, item.notification)
		if err == nil {
			d.opts.Counters.ObserveNotification(true)
			return
		}

		if tentativa == d.opts.MaxAttempts {
			d.dropped.Add(1)
			d.opts.Counters.ObserveNotification(false)
			slog.Error("aviso nao entregue apos todas as tentativas",
				"monitor", item.notification.Monitor.Name,
				"estado", item.notification.Status(),
				"tentativas", tentativa,
				"erro", err)
			return
		}

		slog.Warn("falha ao entregar aviso; nova tentativa em seguida",
			"monitor", item.notification.Monitor.Name,
			"tentativa", tentativa, "erro", err)

		select {
		case <-d.opts.Clock.After(espera):
		case <-ctx.Done():
			// Encerrando: uma última tentativa imediata, porque a espera
			// não cabe mais no prazo de desligamento.
			if err := item.channel.Send(context.Background(), item.notification); err != nil {
				d.dropped.Add(1)
				d.opts.Counters.ObserveNotification(false)
			} else {
				d.opts.Counters.ObserveNotification(true)
			}
			return
		}
		// Espaçamento crescente: se o destino está sobrecarregado,
		// insistir no mesmo ritmo só piora.
		espera *= 2
	}
}
