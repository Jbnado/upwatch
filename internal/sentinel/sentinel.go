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
// Endereços de operadores distintos, para a chance de todos caírem juntos
// ser desprezível perto da chance de o próprio link cair.
//
// A porta é 443, não 53. Muita rede corporativa e muito contêiner bloqueia
// saída na 53 para forçar o resolvedor interno, enquanto 443 é a porta
// mais universalmente liberada que existe. Com alvos na 53, uma rede
// dessas faria a sentinela concluir que está tudo fora do ar e silenciar
// todos os alertas — o pior desfecho possível num sistema de aviso.
var DefaultTargets = []string{
	"1.1.1.1:443",
	"8.8.8.8:443",
	"9.9.9.9:443",
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
	// proven registra se a sentinela já alcançou algum alvo alguma vez.
	proven bool
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

// Proven informa se a sentinela já alcançou algum alvo alguma vez.
//
// Enquanto for falso ela não é levada em conta: uma sentinela que nunca
// funcionou provavelmente está bloqueada ou mal configurada, não diante
// de uma internet inteira fora do ar.
func (s *Sentinel) Proven() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proven
}

// NetworkUp informa se ao menos um alvo respondeu.
//
// Devolve verdadeiro — ou seja, não suprime nada — em dois casos: sem
// alvos configurados, e enquanto a sentinela nunca tiver alcançado nada.
//
// O segundo caso é a salvaguarda mais importante do componente. Se ela
// nunca funcionou, o mais provável não é que a internet inteira esteja
// fora, e sim que a saída esteja bloqueada — uma rede que só libera 443,
// por exemplo. Confiar nela nesse estado faria o UpWatch silenciar todos
// os alertas para sempre, sem que nada indicasse o motivo.
func (s *Sentinel) NetworkUp(ctx context.Context) bool {
	if !s.Enabled() {
		return true
	}

	s.mu.Lock()
	if s.hasCached && s.clock.Now().Sub(s.cachedAt) < s.cacheTTL {
		up, proven := s.cachedUp, s.proven
		s.mu.Unlock()
		return up || !proven
	}
	s.mu.Unlock()

	up := s.probe(ctx)

	s.mu.Lock()
	s.cachedUp, s.cachedAt, s.hasCached = up, s.clock.Now(), true
	if up {
		s.proven = true
	}
	proven := s.proven
	s.mu.Unlock()

	return up || !proven
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
