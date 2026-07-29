package sentinel_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/sentinel"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// dialLog registra as tentativas e decide quais alvos respondem.
type dialLog struct {
	mu        sync.Mutex
	reachable map[string]bool
	attempts  atomic.Int32
	targets   []string
}

func newDialLog(reachable map[string]bool) *dialLog {
	return &dialLog{reachable: reachable}
}

func (d *dialLog) dial(_ context.Context, target string) error {
	d.attempts.Add(1)

	d.mu.Lock()
	d.targets = append(d.targets, target)
	ok := d.reachable[target]
	d.mu.Unlock()

	if ok {
		return nil
	}
	return errors.New("inalcançável")
}

func (d *dialLog) tried() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.targets...)
}

func newSentinel(t *testing.T, d *dialLog, targets []string, ttl time.Duration) (*sentinel.Sentinel, *clock.Fake) {
	t.Helper()

	fake := clock.NewFake(epoch)
	s := sentinel.New(sentinel.Options{
		Targets:  targets,
		Timeout:  time.Second,
		CacheTTL: ttl,
		Clock:    fake,
		Dial:     d.dial,
	})
	return s, fake
}

func TestNetworkUpWhenEveryTargetAnswers(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true, "8.8.8.8:53": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:53", "8.8.8.8:53"}, time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = false with every sentinel answering, want true")
	}
}

// Um único alvo respondendo já prova que a rota para fora existe; exigir
// todos transformaria a queda de um provedor de DNS em alarme geral.
func TestNetworkUpWhenAnySingleTargetAnswers(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": false, "8.8.8.8:53": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:53", "8.8.8.8:53"}, time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = false with one sentinel answering, want true")
	}
}

func TestNetworkDownWhenNoTargetAnswers(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true, "8.8.8.8:53": true})
	s, fake := newSentinel(t, d, []string{"1.1.1.1:53", "8.8.8.8:53"}, time.Second)

	// A sentinela precisa ter funcionado antes para ser levada em conta;
	// uma que nunca alcançou nada não é confiável para calar alertas.
	if !s.NetworkUp(context.Background()) {
		t.Fatal("NetworkUp() = false while the targets were reachable")
	}

	d.mu.Lock()
	d.reachable["1.1.1.1:53"] = false
	d.reachable["8.8.8.8:53"] = false
	d.mu.Unlock()
	fake.Advance(2 * time.Second)

	if s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = true with no sentinel answering, want false")
	}
}

// A propriedade mais importante do componente.
//
// Se a sentinela nunca alcançou nada, o mais provável não é que a internet
// inteira esteja fora — é que ela própria esteja bloqueada ou mal
// configurada. Confiar nela nesse estado faria o UpWatch silenciar todos
// os alertas para sempre, que é o pior desfecho possível num sistema de
// aviso. Ela só ganha o poder de calar depois de provar que funciona.
func TestNeverSucceededSentinelDoesNotSuppress(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:443": false})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:443"}, time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = false before the sentinel ever reached anything; " +
			"an unproven sentinel must not be trusted to silence alerts")
	}
}

// Depois de ter funcionado uma vez, a sentinela passa a valer: agora uma
// falha significa mesmo perda de conectividade.
func TestProvenSentinelSuppressesAfterFailure(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:443": true})
	s, fake := newSentinel(t, d, []string{"1.1.1.1:443"}, time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Fatal("NetworkUp() = false while the target was reachable")
	}

	d.mu.Lock()
	d.reachable["1.1.1.1:443"] = false
	d.mu.Unlock()
	fake.Advance(2 * time.Second)

	if s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = true after a proven sentinel lost the route, want false")
	}
}

func TestProvenReportsWhetherTheSentinelEverWorked(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:443": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:443"}, time.Second)

	if s.Proven() {
		t.Error("Proven() = true before any attempt, want false")
	}

	s.NetworkUp(context.Background())

	if !s.Proven() {
		t.Error("Proven() = false after a successful reach, want true")
	}
}

// Alvos na porta 53 seriam bloqueados por qualquer rede que force o
// resolvedor interno, e a sentinela silenciaria tudo nessas instalações.
func TestDefaultTargetsUseAWidelyPermittedPort(t *testing.T) {
	for _, target := range sentinel.DefaultTargets {
		if !strings.HasSuffix(target, ":443") {
			t.Errorf("default target %q does not use port 443, which is the most "+
				"universally permitted outbound port", target)
		}
	}
}

// Sem alvos a sentinela fica desligada e nunca suprime nada — o
// comportamento anterior ao recurso.
func TestNetworkUpWithoutConfiguredTargets(t *testing.T) {
	d := newDialLog(nil)
	s, _ := newSentinel(t, d, nil, time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = false with no targets configured, want true")
	}
	if d.attempts.Load() != 0 {
		t.Errorf("dialled %d times with no targets, want 0", d.attempts.Load())
	}
}

// Sem cache, cem monitores caindo juntos gerariam cem rodadas de sondagem
// contra os mesmos alvos, justamente quando a rede está ruim.
func TestNetworkStateIsCachedWithinTTL(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:53"}, 10*time.Second)

	for i := 0; i < 20; i++ {
		s.NetworkUp(context.Background())
	}

	if got := d.attempts.Load(); got != 1 {
		t.Errorf("dialled %d times, want 1: the result must be cached", got)
	}
}

func TestNetworkStateIsRecheckedAfterTTL(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true})
	s, fake := newSentinel(t, d, []string{"1.1.1.1:53"}, 10*time.Second)

	s.NetworkUp(context.Background())
	fake.Advance(11 * time.Second)
	s.NetworkUp(context.Background())

	if got := d.attempts.Load(); got != 2 {
		t.Errorf("dialled %d times, want 2: the cache must expire", got)
	}
}

// Quando a rede volta, a supressão precisa acabar na próxima verificação —
// continuar suprimindo esconderia uma queda real do alvo.
func TestNetworkRecoveryIsObserved(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true})
	s, fake := newSentinel(t, d, []string{"1.1.1.1:53"}, time.Second)

	// Estabelece que a sentinela funciona, e então derruba a rota.
	s.NetworkUp(context.Background())

	d.mu.Lock()
	d.reachable["1.1.1.1:53"] = false
	d.mu.Unlock()
	fake.Advance(2 * time.Second)

	if s.NetworkUp(context.Background()) {
		t.Fatal("NetworkUp() = true while the sentinel was unreachable, want false")
	}

	d.mu.Lock()
	d.reachable["1.1.1.1:53"] = true
	d.mu.Unlock()
	fake.Advance(2 * time.Second)

	if !s.NetworkUp(context.Background()) {
		t.Error("NetworkUp() = false after the network came back, want true")
	}
}

// Vários workers consultam a sentinela ao mesmo tempo; uma corrida aqui
// contaminaria a decisão de suprimir ou não o alerta.
func TestNetworkUpIsSafeForConcurrentUse(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:53"}, 10*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.NetworkUp(context.Background())
		}()
	}
	wg.Wait()
}

// Parar no primeiro alvo que responde evita gastar tempo e tráfego com os
// demais no caminho feliz, que é a esmagadora maioria das vezes.
func TestNetworkUpStopsAtTheFirstAnsweringTarget(t *testing.T) {
	d := newDialLog(map[string]bool{"1.1.1.1:53": true, "8.8.8.8:53": true})
	s, _ := newSentinel(t, d, []string{"1.1.1.1:53", "8.8.8.8:53"}, time.Second)

	s.NetworkUp(context.Background())

	if tried := d.tried(); len(tried) != 1 {
		t.Errorf("dialled %v, want it to stop after the first success", tried)
	}
}

func TestDefaultTargetsAreConfigured(t *testing.T) {
	if len(sentinel.DefaultTargets) == 0 {
		t.Fatal("DefaultTargets is empty, want sensible defaults")
	}
	for _, target := range sentinel.DefaultTargets {
		if target == "" {
			t.Error("DefaultTargets contains an empty entry")
		}
	}
}
