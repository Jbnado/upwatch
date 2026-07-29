package scheduler

import (
	"container/heap"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// entry é um monitor agendado com seu próximo vencimento.
type entry struct {
	monitor domain.Monitor
	nextRun time.Time

	// inFlight marca que o check está executando. Enquanto verdadeiro o
	// monitor não é reagendado, o que impede execuções de se acumularem
	// quando o alvo responde mais devagar que o próprio intervalo.
	inFlight bool

	index int // posição no heap, mantida pelo container/heap
}

// dueQueue é uma fila de prioridade por vencimento.
//
// Uma goroutine por monitor seria mais simples, mas com milhares de alvos
// significaria milhares de timers concorrendo; um heap único mantém o
// custo proporcional ao log do número de monitores.
type dueQueue []*entry

func (q dueQueue) Len() int { return len(q) }

func (q dueQueue) Less(i, j int) bool { return q[i].nextRun.Before(q[j].nextRun) }

func (q dueQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *dueQueue) Push(x any) {
	e := x.(*entry)
	e.index = len(*q)
	*q = append(*q, e)
}

func (q *dueQueue) Pop() any {
	old := *q
	n := len(old)
	e := old[n-1]
	old[n-1] = nil // evita segurar o monitor em memória
	e.index = -1
	*q = old[:n-1]
	return e
}

// peek devolve o próximo a vencer sem removê-lo.
func (q dueQueue) peek() *entry {
	if len(q) == 0 {
		return nil
	}
	return q[0]
}

// update reposiciona uma entrada após seu vencimento mudar.
func (q *dueQueue) update(e *entry, nextRun time.Time) {
	e.nextRun = nextRun
	heap.Fix(q, e.index)
}
