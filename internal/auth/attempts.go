package auth

import (
	"crypto/sha256"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

const (
	// maxTrackedAccounts limita a memória do controle de tentativas. Cada
	// registro ocupa poucas dezenas de bytes, então o teto cobre com folga
	// qualquer instalação de verdade; ele existe para quem inventa nomes.
	maxTrackedAccounts = 100_000

	// attemptSweepInterval é de quanto em quanto tempo os registros
	// vencidos são varridos. Varrer a cada tentativa custaria a lista
	// inteira por login; nunca varrer deixaria o mapa só crescer.
	attemptSweepInterval = time.Minute
)

// attemptKey identifica a conta no controle de tentativas.
//
// Hash de tamanho fixo em vez do nome: o registro custa o mesmo para
// qualquer nome digitado, e a memória deixa de depender do que chega.
type attemptKey [sha256.Size]byte

func keyFor(username string) attemptKey {
	return sha256.Sum256([]byte(username))
}

// attemptRecord acompanha as falhas de uma conta.
type attemptRecord struct {
	// failures são as tentativas já recusadas.
	failures int
	// inFlight são as tentativas reservadas ainda em conferência. Contam
	// no limite junto com as falhas: sem elas, pedidos simultâneos seriam
	// todos conferidos antes de o primeiro ser registrado.
	inFlight    int
	lastFailure time.Time
	lockedTill  time.Time
}

// stale informa se o registro já não muda nenhuma decisão: bloqueio
// vencido, ou falhas abaixo do limite paradas há uma janela inteira.
//
// As falhas abaixo do limite também caducam. Sem isso, quem errou a senha
// quatro vezes em março ficaria a um erro do bloqueio para sempre.
func (r *attemptRecord) stale(now time.Time) bool {
	if r.inFlight > 0 {
		return false
	}
	if !r.lockedTill.IsZero() {
		return !now.Before(r.lockedTill)
	}
	return now.Sub(r.lastFailure) >= LockoutWindow
}

// attempts é o controle de tentativas por conta, em memória.
type attempts struct {
	clock clock.Clock

	mu        sync.Mutex
	records   map[attemptKey]*attemptRecord
	lastSweep time.Time
}

func newAttempts(c clock.Clock) *attempts {
	return &attempts{clock: c, records: make(map[attemptKey]*attemptRecord)}
}

// attempt é uma tentativa reservada. Quem a recebe termina com fail,
// succeed ou release — e só uma vez, o que end garante.
type attempt struct {
	a     *attempts
	key   attemptKey
	ended bool
}

// reserve reserva uma tentativa para a conta antes de conferir a
// credencial, ou recusa se a conta já esgotou as dela.
//
// Reservar e conferir o limite na mesma seção crítica é o que fecha a
// brecha dos pedidos em paralelo: o quinto pedido simultâneo já encontra
// as quatro reservas dos outros.
func (a *attempts) reserve(username string) (*attempt, error) {
	key := keyFor(username)
	now := a.clock.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	a.maybeSweep(now)

	rec, ok := a.records[key]
	if ok && rec.stale(now) {
		// Janela vencida: o histórico é descartado para o bloqueio não
		// virar permanente.
		delete(a.records, key)
		ok = false
	}
	if !ok {
		if len(a.records) >= maxTrackedAccounts && !a.evictOne(now) {
			// Mapa cheio só de contas bloqueadas ou em conferência: recusar
			// é o desfecho seguro. Esquecer uma delas devolveria as
			// tentativas a quem acabou de esgotá-las.
			return nil, ErrTooManyAttempts
		}
		rec = &attemptRecord{}
		a.records[key] = rec
	}

	if !rec.lockedTill.IsZero() || rec.failures+rec.inFlight >= MaxLoginAttempts {
		return nil, ErrTooManyAttempts
	}
	rec.inFlight++
	return &attempt{a: a, key: key}, nil
}

// fail registra a tentativa como recusada.
func (t *attempt) fail() {
	t.end(func(rec *attemptRecord, now time.Time) {
		rec.failures++
		rec.lastFailure = now
		if rec.failures >= MaxLoginAttempts {
			rec.lockedTill = now.Add(LockoutWindow)
		}
	})
}

// succeed zera o histórico da conta, para quem errou algumas vezes não
// ficar a poucas tentativas do bloqueio.
func (t *attempt) succeed() {
	t.end(func(rec *attemptRecord, _ time.Time) {
		// Zera em vez de apagar: as outras tentativas em curso ainda
		// precisam do registro para contar quando terminarem.
		*rec = attemptRecord{inFlight: rec.inFlight}
	})
}

// release devolve a reserva sem contar nada: a conferência não chegou a
// um veredito (erro do banco), ou chegou a um que não deve zerar as
// falhas (a senha certa de quem ainda deve o código).
func (t *attempt) release() {
	t.end(func(*attemptRecord, time.Time) {})
}

func (t *attempt) end(apply func(rec *attemptRecord, now time.Time)) {
	if t.ended {
		return
	}
	t.ended = true
	now := t.a.clock.Now()

	t.a.mu.Lock()
	defer t.a.mu.Unlock()

	rec, ok := t.a.records[t.key]
	if !ok {
		return
	}
	rec.inFlight--
	apply(rec, now)
	if rec.inFlight == 0 && rec.failures == 0 && rec.lockedTill.IsZero() {
		delete(t.a.records, t.key)
	}
}

// maybeSweep apaga os registros vencidos, no máximo uma vez por intervalo.
func (a *attempts) maybeSweep(now time.Time) {
	if now.Sub(a.lastSweep) < attemptSweepInterval {
		return
	}
	a.lastSweep = now
	for key, rec := range a.records {
		if rec.stale(now) {
			delete(a.records, key)
		}
	}
}

// evictOne abre espaço no mapa cheio, esquecendo uma conta ainda abaixo
// do limite e sem tentativa em curso. Bloqueadas ficam: esquecê-las
// devolveria as tentativas.
func (a *attempts) evictOne(now time.Time) bool {
	for key, rec := range a.records {
		if rec.stale(now) || (rec.inFlight == 0 && rec.lockedTill.IsZero()) {
			delete(a.records, key)
			return true
		}
	}
	return false
}
