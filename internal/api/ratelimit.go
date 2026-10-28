package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

// Limite do /push por origem.
//
// O endpoint não tem login: sem teto, qualquer um mantém o servidor
// ocupado conferindo tokens, ou tenta adivinhar um. O teto é generoso o
// bastante para uma máquina com dezenas de crons reportando no mesmo
// minuto — a rajada cobre o minuto cheio, e a reposição sustenta cinco por
// segundo.
//
// A origem é o endereço da conexão. Atrás de um proxy reverso todas as
// requisições chegam do proxy e dividem o mesmo balde; confiar num
// cabeçalho como X-Forwarded-For deixaria o próprio atacante escolher a
// origem a cada requisição e anularia o limite.
const (
	pushBurst     = 300
	pushPerSecond = 5

	// maxTrackedClients limita a memória do limitador. Passado o teto, os
	// baldes cheios — de quem não está gastando nada — são esquecidos.
	maxTrackedClients = 10_000
)

// rateLimiter é um balde de fichas por origem, em memória.
//
// Em memória de propósito: o limite protege este processo, e um reinício
// que o zera não abre brecha que valha uma dependência nova.
type rateLimiter struct {
	clock     clock.Clock
	burst     float64
	perSecond float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(c clock.Clock, burst, perSecond float64) *rateLimiter {
	return &rateLimiter{clock: c, burst: burst, perSecond: perSecond, buckets: map[string]*bucket{}}
}

// allow consome uma ficha da origem e devolve, quando não há, quanto
// esperar pela próxima.
func (l *rateLimiter) allow(origin string) (bool, time.Duration) {
	now := l.clock.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[origin]
	if !ok {
		if len(l.buckets) >= maxTrackedClients {
			l.forgetIdle(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[origin] = b
	}

	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSecond)
	b.last = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / l.perSecond * float64(time.Second))
		return false, wait
	}
	b.tokens--
	return true, 0
}

// forgetIdle esquece as origens cujo balde já encheu de novo: lembrar
// delas não muda nenhuma decisão futura.
func (l *rateLimiter) forgetIdle(now time.Time) {
	for origin, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.perSecond >= l.burst {
			delete(l.buckets, origin)
		}
	}
}

// limit recusa com 429 a origem que esgotou o balde.
func (l *rateLimiter) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.allow(clientOrigin(r))
		if !ok {
			// Retry-After em segundos inteiros, arredondado para cima: zero
			// mandaria o cliente tentar de novo na hora.
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, codeTooManyReqs,
				"requisições demais; aguarde antes de tentar novamente")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientOrigin é o endereço da conexão, sem a porta: cada requisição de um
// mesmo cliente pode chegar de uma porta diferente.
func clientOrigin(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
