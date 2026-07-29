// Package sentinel detecta quando é a rede do próprio UpWatch que caiu.
//
// Sem isso, perder conectividade faz todos os monitores falharem ao mesmo
// tempo e dispara uma tempestade de alertas sobre serviços que continuam
// no ar. O operador acorda às três da manhã para descobrir que o problema
// era o link do servidor de monitoramento.
package sentinel

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
)

// DefaultTargets são os alvos consultados para decidir se a rota para fora
// existe.
//
// Resolvedores públicos de operadores distintos: a chance de todos caírem
// juntos é desprezível perto da chance de o próprio link cair.
var DefaultTargets = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
	"9.9.9.9:53",
}

// DefaultCacheTTL é por quanto tempo o veredito é reaproveitado.
//
// Sem cache, cem monitores caindo juntos gerariam cem rodadas de sondagem
// contra os mesmos alvos, justamente quando a rede está ruim.
const DefaultCacheTTL = 10 * time.Second

// DefaultTimeout é o prazo de cada tentativa. Curto de propósito: a
// decisão precisa sair rápido para não atrasar o registro da batida.
const DefaultTimeout = 2 * time.Second

// DialFunc tenta alcançar um alvo.
type DialFunc func(ctx context.Context, target string) error

// Options configura a sentinela. Campos zerados assumem o padrão.
type Options struct {
	// Targets vazio desliga o recurso: nada é suprimido.
	Targets  []string
	Timeout  time.Duration
	CacheTTL time.Duration
	Clock    clock.Clock
	Dial     DialFunc
}

// Sentinel informa se a rede do host parece funcional.
type Sentinel struct {
	targets  []string
	timeout  time.Duration
	cacheTTL time.Duration
	clock    clock.Clock
	dial     DialFunc

	mu        sync.Mutex
	cachedUp  bool
	cachedAt  time.Time
	hasCached bool
}

// New cria a sentinela.
func New(opts Options) *Sentinel {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Dial == nil {
		opts.Dial = tcpDial
	}
	return &Sentinel{
		targets:  opts.Targets,
		timeout:  opts.Timeout,
		cacheTTL: opts.CacheTTL,
		clock:    opts.Clock,
		dial:     opts.Dial,
	}
}

// Enabled informa se a sentinela tem alvos para consultar.
func (s *Sentinel) Enabled() bool { return len(s.targets) > 0 }

// NetworkUp informa se ao menos um alvo respondeu.
//
// Sem alvos configurados devolve verdadeiro, mantendo o comportamento
// anterior ao recurso: nada é suprimido.
func (s *Sentinel) NetworkUp(ctx context.Context) bool {
	if !s.Enabled() {
		return true
	}

	s.mu.Lock()
	if s.hasCached && s.clock.Now().Sub(s.cachedAt) < s.cacheTTL {
		up := s.cachedUp
		s.mu.Unlock()
		return up
	}
	s.mu.Unlock()

	up := s.probe(ctx)

	s.mu.Lock()
	s.cachedUp, s.cachedAt, s.hasCached = up, s.clock.Now(), true
	s.mu.Unlock()

	return up
}

// probe tenta os alvos em ordem, parando no primeiro que responde.
//
// Um único alvo alcançável já prova que a rota para fora existe; exigir
// todos transformaria a queda de um provedor de DNS em alarme geral.
func (s *Sentinel) probe(ctx context.Context) bool {
	for _, target := range s.targets {
		attemptCtx, cancel := context.WithTimeout(ctx, s.timeout)
		err := s.dial(attemptCtx, target)
		cancel()

		if err == nil {
			return true
		}
	}
	return false
}

// tcpDial abre e fecha uma conexão TCP.
//
// Um aperto de mão TCP basta: prova que há rota e que algo respondeu do
// outro lado, sem depender de o protocolo do alvo estar funcionando.
func tcpDial(ctx context.Context, target string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return err
	}
	return conn.Close()
}
