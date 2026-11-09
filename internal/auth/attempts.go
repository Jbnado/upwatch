package auth

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

const (
	maxTrackedPairs      = 100_000
	maxTrackedOrigins    = 20_000
	maxTrackedAccounts   = 20_000
	maxOriginAccounts    = 20
	maxAccountOrigins    = 3
	knownPairTTL         = 30 * 24 * time.Hour
	attemptSweepInterval = time.Minute

	AttemptScopePair    = "par"
	AttemptScopeOrigin  = "origem"
	AttemptScopeAccount = "conta"
)

// AttemptLimitError identifica o escopo apenas para o registro interno;
// a resposta pública continua sendo ErrTooManyAttempts em todos os casos.
type AttemptLimitError struct{ Scope, Username string }

func (e *AttemptLimitError) Error() string { return ErrTooManyAttempts.Error() }
func (e *AttemptLimitError) Unwrap() error { return ErrTooManyAttempts }

type attemptKey [sha256.Size]byte

func keyFor(value string) attemptKey { return sha256.Sum256([]byte(value)) }

type pairKey struct{ account, origin attemptKey }

type attemptRecord struct {
	failures    int
	inFlight    int
	lastFailure time.Time
	knownTill   time.Time
	idle        *list.Element
}

func (r *attemptRecord) known(now time.Time) bool { return now.Before(r.knownTill) }
func (r *attemptRecord) expire(now time.Time) {
	if !r.lastFailure.IsZero() && now.Sub(r.lastFailure) >= LockoutWindow {
		r.failures = 0
		r.lastFailure = time.Time{}
	}
}
func (r *attemptRecord) stale(now time.Time) bool {
	r.expire(now)
	return r.inFlight == 0 && r.failures == 0 && !r.known(now)
}

// Cada conjunto retém só os membros necessários para fechar seu escopo.
// A validade é individual: atividade contra outra conta não renova a antiga.
type aggregateRecord struct {
	key     attemptKey
	members map[attemptKey]time.Time
	idle    *list.Element
}
type aggregateRecords struct {
	records   map[attemptKey]*aggregateRecord
	idle      list.List
	capacity  int
	threshold int
}

func newAggregateRecords(capacity, threshold int) aggregateRecords {
	return aggregateRecords{records: make(map[attemptKey]*aggregateRecord), capacity: capacity, threshold: threshold}
}
func (a *aggregateRecords) remove(rec *aggregateRecord) {
	delete(a.records, rec.key)
	if rec.idle != nil {
		a.idle.Remove(rec.idle)
	}
}
func (a *aggregateRecords) syncIdle(rec *aggregateRecord) {
	if len(rec.members) >= a.threshold {
		if rec.idle != nil {
			a.idle.Remove(rec.idle)
			rec.idle = nil
		}
	} else if rec.idle == nil {
		rec.idle = a.idle.PushBack(rec)
	} else {
		a.idle.MoveToBack(rec.idle)
	}
}
func (a *aggregateRecords) get(key attemptKey, now time.Time) *aggregateRecord {
	rec := a.records[key]
	if rec == nil {
		return nil
	}
	for member, failedAt := range rec.members {
		if now.Sub(failedAt) >= LockoutWindow {
			delete(rec.members, member)
		}
	}
	if len(rec.members) == 0 {
		a.remove(rec)
		return nil
	}
	a.syncIdle(rec)
	return rec
}
func (a *aggregateRecords) blocked(key attemptKey, now time.Time) bool {
	rec := a.get(key, now)
	if rec != nil {
		return len(rec.members) >= a.threshold
	}
	// Quando todos os escopos estão bloqueados, recusar um novo escopo
	// conserva a janela de proteção em vez de despejar um bloqueio ativo.
	return len(a.records) >= a.capacity && a.idle.Len() == 0
}
func (a *aggregateRecords) add(key, member attemptKey, now time.Time) {
	rec := a.get(key, now)
	if rec == nil {
		if len(a.records) >= a.capacity {
			oldest := a.idle.Front()
			if oldest == nil {
				return
			}
			a.remove(oldest.Value.(*aggregateRecord))
		}
		rec = &aggregateRecord{key: key, members: make(map[attemptKey]time.Time)}
		a.records[key] = rec
	}
	if _, exists := rec.members[member]; exists || len(rec.members) < a.threshold {
		rec.members[member] = now
	}
	a.syncIdle(rec)
}
func (a *aggregateRecords) forget(key, member attemptKey, now time.Time) {
	if rec := a.get(key, now); rec != nil {
		delete(rec.members, member)
		if len(rec.members) == 0 {
			a.remove(rec)
		} else {
			a.syncIdle(rec)
		}
	}
}

// Os três mapas têm tetos próprios. A lista contém somente pares sem
// conferência em curso, bloqueio ou confiança ativa, permitindo despejo
// determinístico em O(1) sem antecipar a liberação de uma conta.
type attempts struct {
	clock     clock.Clock
	mu        sync.Mutex
	records   map[pairKey]*attemptRecord
	idle      list.List
	origins   aggregateRecords
	accounts  aggregateRecords
	lastSweep time.Time
}

func newAttempts(c clock.Clock) *attempts {
	return &attempts{clock: c, records: make(map[pairKey]*attemptRecord),
		origins:  newAggregateRecords(maxTrackedOrigins, maxOriginAccounts),
		accounts: newAggregateRecords(maxTrackedAccounts, maxAccountOrigins)}
}

type attempt struct {
	a     *attempts
	key   pairKey
	ended bool
}

// A reserva ocupa o orçamento antes da consulta ao banco e é liberada só
// pelo resultado. Nem sucesso nem expiração esquecem reservas concorrentes.
func (a *attempts) reserve(username, origin string) (*attempt, error) {
	key := pairKey{account: keyFor(username), origin: keyFor(origin)}
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maybeSweep(now)
	rec := a.records[key]
	if rec != nil {
		rec.expire(now)
	}
	if rec != nil && rec.failures+rec.inFlight >= MaxLoginAttempts {
		return nil, &AttemptLimitError{Scope: AttemptScopePair, Username: username}
	}
	if rec == nil || !rec.known(now) {
		if a.origins.blocked(key.origin, now) {
			return nil, &AttemptLimitError{Scope: AttemptScopeOrigin, Username: username}
		}
		if a.accounts.blocked(key.account, now) {
			return nil, &AttemptLimitError{Scope: AttemptScopeAccount, Username: username}
		}
	}
	if rec == nil {
		if len(a.records) >= maxTrackedPairs {
			oldest := a.idle.Front()
			// Sem par descartável, recusar capacidade preserva bloqueios e
			// confiança até que uma janela vença ou uma conferência termine.
			if oldest == nil {
				return nil, &AttemptLimitError{Scope: AttemptScopePair, Username: username}
			}
			a.remove(oldest.Value.(pairKey))
		}
		rec = &attemptRecord{}
		a.records[key] = rec
	}
	if rec.idle != nil {
		a.idle.Remove(rec.idle)
		rec.idle = nil
	}
	rec.inFlight++
	return &attempt{a: a, key: key}, nil
}
func (a *attempts) remove(key pairKey) {
	rec := a.records[key]
	if rec.idle != nil {
		a.idle.Remove(rec.idle)
	}
	delete(a.records, key)
}
func (t *attempt) fail() {
	t.end(func(rec *attemptRecord, now time.Time) {
		rec.failures++
		rec.lastFailure = now
		if rec.known(now) {
			return
		}
		t.a.origins.add(t.key.origin, t.key.account, now)
		if rec.failures >= MaxLoginAttempts {
			t.a.accounts.add(t.key.account, t.key.origin, now)
		}
	})
}

// O sucesso completo conhece o par, sem apagar o ataque de outras origens
// nem a varredura de outros nomes. Reservas concorrentes continuam ocupadas.
func (t *attempt) succeed() {
	t.end(func(rec *attemptRecord, now time.Time) {
		rec.failures = 0
		rec.lastFailure = time.Time{}
		rec.knownTill = now.Add(knownPairTTL)
		t.a.origins.forget(t.key.origin, t.key.account, now)
		t.a.accounts.forget(t.key.account, t.key.origin, now)
	})
}
func (t *attempt) release() { t.end(func(*attemptRecord, time.Time) {}) }
func (t *attempt) end(apply func(*attemptRecord, time.Time)) {
	t.a.mu.Lock()
	defer t.a.mu.Unlock()
	if t.ended {
		return
	}
	t.ended = true
	now := t.a.clock.Now()
	rec := t.a.records[t.key]
	rec.inFlight--
	rec.expire(now)
	apply(rec, now)
	if rec.stale(now) {
		t.a.remove(t.key)
		return
	}
	if rec.inFlight == 0 && rec.failures < MaxLoginAttempts && !rec.known(now) {
		rec.idle = t.a.idle.PushBack(t.key)
	}
}

// A varredura é periódica; o despejo por pressão de memória não percorre mapa.
func (a *attempts) maybeSweep(now time.Time) {
	if now.Sub(a.lastSweep) < attemptSweepInterval {
		return
	}
	a.lastSweep = now
	for key, rec := range a.records {
		if rec.stale(now) {
			a.remove(key)
		}
	}
	for key := range a.origins.records {
		a.origins.get(key, now)
	}
	for key := range a.accounts.records {
		a.accounts.get(key, now)
	}
}
