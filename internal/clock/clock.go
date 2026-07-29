// Package clock abstrai a passagem do tempo.
//
// Nenhum código de produção do UpWatch chama time.Now diretamente: o
// scheduler, o worker de rollup e a máquina de estado de incidentes
// recebem um Clock. Em teste, Fake avança o tempo instantaneamente — sem
// isso, verificar "roda a cada 60s" ou "apaga dado de 8 dias atrás"
// exigiria sleeps reais, tornando a suíte lenta e instável.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Timer espelha o contrato de *time.Timer, com o mínimo que o UpWatch usa.
type Timer interface {
	// C devolve o canal em que o vencimento é entregue.
	C() <-chan time.Time
	// Stop cancela o timer e informa se ele ainda estava pendente.
	Stop() bool
	// Reset reagenda o timer e informa se ele ainda estava pendente.
	Reset(d time.Duration) bool
}

// Clock é a fonte de tempo injetada nos componentes.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
	After(d time.Duration) <-chan time.Time
}

// ---------- relógio real ----------

type realClock struct{}

// Real devolve o relógio apoiado no pacote time, usado em produção.
func Real() Clock { return realClock{} }

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer {
	return &realTimer{t: time.NewTimer(d)}
}

func (realClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

type realTimer struct{ t *time.Timer }

func (r *realTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// ---------- relógio falso ----------

// blocker é uma espera por uma condição sobre os timers pendentes.
type blocker struct {
	ready func(pending []*fakeTimer) bool
	done  chan struct{}
}

// Fake é um Clock controlado manualmente pelos testes.
//
// É seguro para uso concorrente: o scheduler mexe no relógio a partir de
// várias goroutines e uma corrida aqui contaminaria qualquer teste que
// dependa de tempo.
type Fake struct {
	mu       sync.Mutex
	now      time.Time
	pending  []*fakeTimer
	blockers []blocker
}

// NewFake cria um relógio falso posicionado em start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start}
}

// Now devolve o instante atual do relógio falso.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer agenda um timer para daqui a d.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()

	t := &fakeTimer{
		clock:    f,
		deadline: f.now.Add(d),
		ch:       make(chan time.Time, 1),
	}
	if f.armLocked(t) {
		f.releaseBlockersLocked()
	}
	return t
}

// armLocked enfileira o timer, ou o dispara na hora se já estiver vencido.
//
// Duração não positiva significa "já venceu", e é o time.Timer real
// dispara de imediato nesse caso. Exigir um Advance aqui travaria para
// sempre qualquer laço que arme um timer de zero para trabalho atrasado.
// Devolve se o timer ficou pendente. Exige f.mu travado.
func (f *Fake) armLocked(t *fakeTimer) bool {
	if t.deadline.After(f.now) {
		f.pending = append(f.pending, t)
		return true
	}
	t.fire()
	return false
}

// After é o atalho equivalente a NewTimer(d).C().
func (f *Fake) After(d time.Duration) <-chan time.Time {
	return f.NewTimer(d).C()
}

// Advance move o relógio para frente e dispara todo timer vencido, em
// ordem de vencimento.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.now = f.now.Add(d)

	var due, still []*fakeTimer
	for _, t := range f.pending {
		if t.deadline.After(f.now) {
			still = append(still, t)
			continue
		}
		due = append(due, t)
	}
	f.pending = still

	sort.Slice(due, func(i, j int) bool { return due[i].deadline.Before(due[j].deadline) })

	for _, t := range due {
		t.fire()
	}
}

// BlockUntil espera até haver pelo menos n timers pendentes.
//
// Evita a corrida clássica de relógio falso: dar Advance antes de a
// goroutine sob teste ter registrado seu timer.
func (f *Fake) BlockUntil(n int) {
	f.block(func(pending []*fakeTimer) bool { return len(pending) >= n })
}

// BlockUntilDeadline espera até haver um timer pendente que vença em at ou
// antes.
//
// Contar timers não basta para sincronizar com um laço que mantém sempre
// um armado: BlockUntil(1) já estaria satisfeito pelo timer da volta
// anterior, e o Advance seguinte dispararia antes do rearme. Esperar pelo
// vencimento específico elimina essa ambiguidade.
func (f *Fake) BlockUntilDeadline(at time.Time) {
	f.block(func(pending []*fakeTimer) bool {
		for _, t := range pending {
			if !t.deadline.After(at) {
				return true
			}
		}
		return false
	})
}

func (f *Fake) block(ready func([]*fakeTimer) bool) {
	f.mu.Lock()
	if ready(f.pending) {
		f.mu.Unlock()
		return
	}
	done := make(chan struct{})
	f.blockers = append(f.blockers, blocker{ready: ready, done: done})
	f.mu.Unlock()

	<-done
}

// releaseBlockersLocked libera quem já teve sua condição satisfeita.
// Exige f.mu travado.
func (f *Fake) releaseBlockersLocked() {
	remaining := f.blockers[:0]
	for _, b := range f.blockers {
		if b.ready(f.pending) {
			close(b.done)
			continue
		}
		remaining = append(remaining, b)
	}
	f.blockers = remaining
}

type fakeTimer struct {
	clock    *Fake
	deadline time.Time
	ch       chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

// fire entrega o vencimento sem bloquear, como faz o time.Timer real.
//
// O valor é o instante do vencimento, não o "agora" após um salto: quem
// calcula o próximo disparo a partir dele acumularia deriva se recebesse
// o tempo corrente.
func (t *fakeTimer) fire() {
	select {
	case t.ch <- t.deadline:
	default:
	}
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.clock.removeLocked(t)
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()

	wasPending := t.clock.removeLocked(t)
	t.deadline = t.clock.now.Add(d)
	if t.clock.armLocked(t) {
		t.clock.releaseBlockersLocked()
	}
	return wasPending
}

// removeLocked tira o timer da fila de pendentes e informa se ele estava
// lá. Exige clock.mu travado.
func (f *Fake) removeLocked(target *fakeTimer) bool {
	for i, t := range f.pending {
		if t != target {
			continue
		}
		f.pending = append(f.pending[:i], f.pending[i+1:]...)
		return true
	}
	return false
}
