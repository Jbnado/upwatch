package api

import (
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
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
// A origem é o endereço da conexão (ver originResolver).
const (
	pushBurst     = 300
	pushPerSecond = 5

	// Limite do POST /setup por origem. O token gerado não se adivinha,
	// mas o escolhido à mão em UPWATCH_SETUP_TOKEN pode ser fraco; com dez
	// tentativas de rajada e uma a cada meio minuto depois, o dono que
	// errou a cópia termina sem esperar, e um chute vira dias por milhar.
	setupBurst     = 10
	setupPerSecond = 1.0 / 30

	// Limite do login e do segundo passo por origem, num balde só: um
	// login com dois fatores gasta duas fichas. O bloqueio por conta não
	// segura quem espalha palpites por muitas contas; isto segura. A
	// rajada cobre um escritório inteiro chegando atrás do mesmo endereço,
	// e a reposição, dez por minuto, deixa o chute em lote sem graça.
	loginBurst     = 30
	loginPerSecond = 1.0 / 6

	// ipv6PrefixBits é o tamanho do bloco IPv6 que conta como uma origem.
	// Um /64 é o que um provedor entrega a um único cliente: contar cada
	// endereço dele à parte daria a cada um trilhões de baldes cheios.
	ipv6PrefixBits = 64

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
	origins   originResolver
	burst     float64
	perSecond float64

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(c clock.Clock, origins originResolver, burst, perSecond float64) *rateLimiter {
	return &rateLimiter{
		clock: c, origins: origins, burst: burst, perSecond: perSecond,
		buckets: map[string]*bucket{},
	}
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
		ok, wait := l.allow(l.origins.origin(r))
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

// originResolver decide de onde veio uma requisição.
//
// Sem proxies confiáveis, a origem é o endereço da conexão, e ponto.
// Atrás de um proxy reverso, todas as requisições chegam dele e dividiriam
// um balde só; por isso o operador pode declarar os proxies dele
// (UPWATCH_TRUSTED_PROXIES), e só quando a conexão vem de um deles o
// X-Forwarded-For é lido. Ler o cabeçalho de qualquer um deixaria o
// próprio atacante escolher a origem a cada requisição e anularia o
// limite.
type originResolver struct {
	trusted []netip.Prefix
}

// origin é a chave do balde: o endereço IPv4, ou o /64 do IPv6.
func (o originResolver) origin(r *http.Request) string {
	addr, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if o.isTrusted(addr) {
		addr = o.forwardedClient(r, addr)
	}
	if addr.Is6() {
		return netip.PrefixFrom(addr, ipv6PrefixBits).Masked().String()
	}
	return addr.String()
}

// forwardedClient percorre o X-Forwarded-For da direita para a esquerda e
// para no primeiro endereço que não é proxy confiável: é o cliente. Os
// da esquerda dele foram escritos pelo próprio cliente e não valem nada.
//
// Se todos forem proxies, vale o mais à esquerda; se um não se ler como
// endereço, vale o último proxy lido, que é o que se sabe com certeza.
func (o originResolver) forwardedClient(r *http.Request, peer netip.Addr) netip.Addr {
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}

	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseAddr(strings.TrimSpace(hops[i]))
		if !ok {
			return client
		}
		client = addr
		if !o.isTrusted(addr) {
			return client
		}
	}
	return client
}

func (o originResolver) isTrusted(addr netip.Addr) bool {
	for _, p := range o.trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// parseAddr lê um endereço com ou sem porta. IPv4 escrito como IPv6
// ("::ffff:1.2.3.4") volta como IPv4, para cair no mesmo balde.
func parseAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().WithZone("").Unmap(), true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	// Zona ("fe80::1%eth0") não muda quem é o cliente, e com ela cada
	// interface viraria um balde.
	return addr.WithZone("").Unmap(), true
}
