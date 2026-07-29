package store

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// Padrões do batch writer.
const (
	// DefaultMaxBatch é o tamanho de lote que dispara gravação imediata.
	DefaultMaxBatch = 500
	// DefaultFlushInterval garante que uma batida solitária não fique
	// represada esperando o lote encher.
	DefaultFlushInterval = time.Second
	// DefaultQueueSize dá folga para rajadas sem bloquear os workers.
	DefaultQueueSize = 4096
	// DefaultMaxBacklog limita o que fica retido quando o banco falha,
	// para indisponibilidade prolongada não virar vazamento de memória.
	DefaultMaxBacklog = 50_000
)

// BatchWriterOptions configura o writer. Campos zerados assumem o padrão.
type BatchWriterOptions struct {
	MaxBatch   int
	Interval   time.Duration
	QueueSize  int
	MaxBacklog int
	Clock      clock.Clock
}

func (o BatchWriterOptions) withDefaults() BatchWriterOptions {
	if o.MaxBatch <= 0 {
		o.MaxBatch = DefaultMaxBatch
	}
	if o.Interval <= 0 {
		o.Interval = DefaultFlushInterval
	}
	if o.QueueSize <= 0 {
		o.QueueSize = DefaultQueueSize
	}
	if o.MaxBacklog <= 0 {
		o.MaxBacklog = DefaultMaxBacklog
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	return o
}

// BatchWriter agrupa batidas e as grava em lote.
//
// É a decisão de desempenho central do UpWatch. Com mil monitores a cada
// 30 segundos são cerca de 33 escritas por segundo; gravadas uma a uma
// seriam 33 transações e 33 fsyncs por segundo, o que o SQLite não
// sustenta. Agrupadas, viram aproximadamente uma transação por segundo.
type BatchWriter struct {
	store TimeseriesStore
	opts  BatchWriterOptions

	in chan domain.Heartbeat

	// buf só é tocado pela goroutine de Run, então dispensa trava.
	buf []domain.Heartbeat

	dropped atomic.Int64
	pending atomic.Int64
}

// NewBatchWriter cria o writer. É preciso chamar Run para ele operar.
func NewBatchWriter(ts TimeseriesStore, opts BatchWriterOptions) *BatchWriter {
	opts = opts.withDefaults()
	return &BatchWriter{
		store: ts,
		opts:  opts,
		in:    make(chan domain.Heartbeat, opts.QueueSize),
		buf:   make([]domain.Heartbeat, 0, opts.MaxBatch),
	}
}

// MaxBatch é o tamanho de lote que dispara gravação imediata.
func (w *BatchWriter) MaxBatch() int { return w.opts.MaxBatch }

// Interval é o período do flush por tempo.
func (w *BatchWriter) Interval() time.Duration { return w.opts.Interval }

// Dropped conta as batidas descartadas por estouro do backlog.
func (w *BatchWriter) Dropped() int64 { return w.dropped.Load() }

// Pending é quanto está retido aguardando gravação.
func (w *BatchWriter) Pending() int64 { return w.pending.Load() }

// Submit entrega uma batida ao writer.
//
// Bloqueia enquanto a fila estiver cheia, aplicando contrapressão, mas
// desiste quando o contexto termina: um worker do agendador preso aqui
// deixaria de verificar seu monitor.
func (w *BatchWriter) Submit(ctx context.Context, hb domain.Heartbeat) error {
	select {
	case w.in <- hb.Normalize():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("store: fila de gravação cheia: %w", ctx.Err())
	}
}

// Run opera o writer até o contexto terminar, gravando o que restou antes
// de sair.
func (w *BatchWriter) Run(ctx context.Context) {
	timer := w.opts.Clock.NewTimer(w.opts.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			w.drainQueue()
			// O contexto de desligamento já está cancelado; repassá-lo ao
			// banco faria a gravação final falhar justamente quando mais
			// importa preservar o que foi medido.
			w.flushAll(context.WithoutCancel(ctx))
			return

		case hb := <-w.in:
			w.buf = append(w.buf, hb)
			w.pending.Store(int64(len(w.buf)))
			if len(w.buf) >= w.opts.MaxBatch {
				w.flush(ctx)
				timer.Reset(w.opts.Interval)
			}

		case <-timer.C():
			w.flush(ctx)
			timer.Reset(w.opts.Interval)
		}
	}
}

// drainQueue move para o buffer o que ainda está na fila, sem bloquear.
func (w *BatchWriter) drainQueue() {
	for {
		select {
		case hb := <-w.in:
			w.buf = append(w.buf, hb)
		default:
			w.pending.Store(int64(len(w.buf)))
			return
		}
	}
}

// flushAll esvazia o buffer, gravando quantos lotes forem necessários.
//
// Usado no desligamento, onde deixar resto para trás perderia medições.
// Para na ausência de progresso: com o banco fora do ar, insistir viraria
// laço infinito segurando o encerramento do processo.
func (w *BatchWriter) flushAll(ctx context.Context) {
	for len(w.buf) > 0 {
		before := len(w.buf)
		w.flush(ctx)
		if len(w.buf) >= before {
			return
		}
	}
}

// flush grava o buffer.
//
// Em caso de falha o lote é retido para a próxima tentativa: descartar
// apagaria medições já feitas, e é justamente durante uma indisponibilidade
// do banco que o histórico importa. A retenção tem teto para que uma queda
// prolongada não vire consumo ilimitado de memória.
func (w *BatchWriter) flush(ctx context.Context) {
	if len(w.buf) == 0 {
		return
	}

	batch := w.buf
	if len(batch) > w.opts.MaxBatch {
		batch = batch[:w.opts.MaxBatch]
	}

	if err := w.store.WriteHeartbeats(ctx, batch); err != nil {
		w.retain()
		return
	}

	// Remove do buffer só o que foi gravado, preservando o excedente para
	// o próximo flush.
	remaining := copy(w.buf, w.buf[len(batch):])
	w.buf = w.buf[:remaining]
	w.pending.Store(int64(len(w.buf)))
}

// retain apara o backlog ao teto, descartando o mais antigo.
//
// O mais antigo sai primeiro porque, numa indisponibilidade longa, o dado
// recente é o que ainda descreve o estado atual dos serviços.
func (w *BatchWriter) retain() {
	if excess := len(w.buf) - w.opts.MaxBacklog; excess > 0 {
		w.buf = w.buf[:copy(w.buf, w.buf[excess:])]
		w.dropped.Add(int64(excess))
	}
	w.pending.Store(int64(len(w.buf)))
}
