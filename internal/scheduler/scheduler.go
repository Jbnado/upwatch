// Package scheduler decide quando cada monitor é verificado.
//
// Uma fila de prioridade por vencimento alimenta um pool de workers de
// tamanho fixo. O teto de workers é o ponto central: sem ele, mil monitores
// vencendo juntos abririam mil conexões simultâneas e esgotariam os
// descritores de arquivo do processo.
package scheduler

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
)

// DefaultWorkers é o teto padrão de checks simultâneos.
const DefaultWorkers = 50

// idleWait é quanto o laço dorme quando não há nada agendado. O timer é
// armado mesmo ocioso para manter previsível a contagem de espera que os
// testes usam para sincronizar com o relógio falso.
const idleWait = time.Hour

// Sink recebe as batidas produzidas pelas verificações.
//
// Interface estreita de propósito: o agendador não precisa conhecer o
// store, apenas para onde mandar o resultado.
type Sink interface {
	Submit(ctx context.Context, hb domain.Heartbeat) error
}

// Options configura o agendador. Campos zerados assumem o padrão.
type Options struct {
	Workers int
	Clock   clock.Clock
	// DisableJitter faz o primeiro check de cada monitor vencer
	// imediatamente. Existe para os testes; em produção o espalhamento
	// evita picos periódicos de carga.
	DisableJitter bool
}

// Scheduler executa os checks no ritmo de cada monitor.
type Scheduler struct {
	registry *checker.Registry
	sink     Sink
	clock    clock.Clock
	workers  int
	jitter   bool

	mu    sync.Mutex
	queue dueQueue
	index map[int64]*entry

	// wake avisa o laço de que a fila mudou. Buffer de um: sinais
	// coincidentes colapsam, já que o laço reavalia a fila inteira.
	wake chan struct{}
}

// New cria o agendador. É preciso chamar Run para ele operar.
func New(reg *checker.Registry, sink Sink, opts Options) *Scheduler {
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	return &Scheduler{
		registry: reg,
		sink:     sink,
		clock:    opts.Clock,
		workers:  opts.Workers,
		jitter:   !opts.DisableJitter,
		index:    make(map[int64]*entry),
		wake:     make(chan struct{}, 1),
	}
}

// Workers é o teto de checks simultâneos.
func (s *Scheduler) Workers() int { return s.workers }

// NextRun devolve quando o monitor será verificado, e se ele está mesmo
// agendado. Alimenta o "próximo check em ..." da interface.
func (s *Scheduler) NextRun(id int64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.index[id]
	if !ok {
		return time.Time{}, false
	}
	return e.nextRun, true
}

// Len é a quantidade de monitores agendados.
func (s *Scheduler) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.index)
}

// Upsert insere ou atualiza um monitor.
//
// Monitor desabilitado é removido da fila: continuar sondando um alvo em
// manutenção geraria alerta que o operador já sabe ser falso.
func (s *Scheduler) Upsert(m domain.Monitor) {
	s.mu.Lock()

	if !m.Enabled {
		s.removeLocked(m.ID)
		s.mu.Unlock()
		s.signal()
		return
	}

	if existing, ok := s.index[m.ID]; ok {
		intervalChanged := existing.monitor.Interval != m.Interval
		existing.monitor = m
		if intervalChanged && !existing.inFlight {
			s.queue.update(existing, s.clock.Now().Add(m.Interval))
		}
		s.mu.Unlock()
		s.signal()
		return
	}

	e := &entry{monitor: m, nextRun: s.clock.Now().Add(s.firstDelay(m))}
	s.index[m.ID] = e
	heap.Push(&s.queue, e)

	s.mu.Unlock()
	s.signal()
}

// Remove tira o monitor do agendamento.
func (s *Scheduler) Remove(id int64) {
	s.mu.Lock()
	s.removeLocked(id)
	s.mu.Unlock()
	s.signal()
}

func (s *Scheduler) removeLocked(id int64) {
	e, ok := s.index[id]
	if !ok {
		return
	}
	delete(s.index, id)
	if e.index >= 0 {
		heap.Remove(&s.queue, e.index)
	}
}

// firstDelay espalha o primeiro check dentro do intervalo do monitor.
//
// O deslocamento vem do id, não de um sorteio: é determinístico, então o
// comportamento se repete entre reinícios e é verificável em teste, e
// ainda assim distribui monitores de mesmo intervalo ao longo da janela.
func (s *Scheduler) firstDelay(m domain.Monitor) time.Duration {
	if !s.jitter {
		return 0
	}
	const buckets = 1000
	slot := m.ID % buckets
	return time.Duration(int64(m.Interval) * slot / buckets)
}

// signal acorda o laço sem bloquear quem chamou.
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run opera o agendador até o contexto terminar, aguardando os checks em
// andamento antes de retornar.
func (s *Scheduler) Run(ctx context.Context) {
	// Semáforo com capacidade fixa: é o que trava a concorrência de saída.
	slots := make(chan struct{}, s.workers)
	var wg sync.WaitGroup

	timer := s.clock.NewTimer(idleWait)
	defer timer.Stop()

	for {
		timer.Reset(s.waitFor())

		select {
		case <-ctx.Done():
			// Espera os checks em andamento: encerrar no meio perderia
			// medições já iniciadas.
			wg.Wait()
			return

		case <-s.wake:
			continue

		case <-timer.C():
			for _, e := range s.takeDue() {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					s.reschedule(e)
					wg.Wait()
					return
				}

				wg.Add(1)
				go func(e *entry) {
					defer wg.Done()
					defer func() { <-slots }()

					s.runCheck(ctx, e.monitor)
					s.reschedule(e)
					s.signal()
				}(e)
			}
		}
	}
}

// waitFor calcula quanto falta para o próximo vencimento.
func (s *Scheduler) waitFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.queue.peek()
	if next == nil {
		return idleWait
	}
	if d := next.nextRun.Sub(s.clock.Now()); d > 0 {
		return d
	}
	return 0
}

// takeDue remove da fila tudo o que já venceu.
//
// As entradas saem do heap enquanto executam e só voltam ao terminar, o
// que é o que impede execuções de se acumularem sobre um alvo lento.
func (s *Scheduler) takeDue() []*entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	var due []*entry
	for {
		next := s.queue.peek()
		if next == nil || next.nextRun.After(now) {
			return due
		}
		e := heap.Pop(&s.queue).(*entry)
		e.inFlight = true
		due = append(due, e)
	}
}

// reschedule devolve a entrada à fila com o próximo vencimento.
//
// A base é o instante atual, não o vencimento anterior: um check demorado
// não deve gerar uma rajada de execuções atrasadas para compensar.
func (s *Scheduler) reschedule(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.inFlight = false

	// Removido ou desabilitado enquanto executava: não volta à fila.
	if current, ok := s.index[e.monitor.ID]; !ok || current != e {
		return
	}

	e.nextRun = s.clock.Now().Add(e.monitor.Interval)
	heap.Push(&s.queue, e)
}

// runCheck executa a verificação e entrega a batida.
func (s *Scheduler) runCheck(ctx context.Context, m domain.Monitor) {
	checkCtx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	res := s.registry.Check(checkCtx, m)

	hb := domain.Heartbeat{
		MonitorID: m.ID,
		Timestamp: s.clock.Now(),
		Status:    res.Status,
		LatencyMS: res.LatencyMS,
		Message:   res.Message,
	}

	// O contexto do check pode já ter estourado; entregar a batida usando
	// um prazo próprio evita perder justamente o registro da falha.
	submitCtx, submitCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer submitCancel()
	_ = s.sink.Submit(submitCtx, hb)
}
