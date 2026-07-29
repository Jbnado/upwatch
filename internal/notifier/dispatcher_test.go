package notifier_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/notifier"
)

// channel é um destino controlável.
type channel struct {
	name string

	calls   atomic.Int32
	failFor atomic.Int32 // quantas entregas ainda vão falhar
	hold    chan struct{}

	mu       sync.Mutex
	received []notifier.Notification
}

func newChannel(name string) *channel {
	return &channel{name: name}
}

func (c *channel) Send(ctx context.Context, n notifier.Notification) error {
	c.calls.Add(1)

	if c.hold != nil {
		select {
		case <-c.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.failFor.Load() > 0 {
		c.failFor.Add(-1)
		return errors.New("canal indisponível")
	}

	c.mu.Lock()
	c.received = append(c.received, n)
	c.mu.Unlock()
	return nil
}

func (c *channel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.received)
}

// runDispatcher sobe o despachante e garante o encerramento.
func runDispatcher(t *testing.T, opts notifier.DispatcherOptions) (*notifier.Dispatcher, func()) {
	t.Helper()

	d := notifier.NewDispatcher(opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		d.Run(ctx)
	}()

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return d, stop
}

// await espera uma condição sem prender o teste indefinidamente.
func await(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDispatcherDelivers(t *testing.T) {
	c := newChannel("discord")
	d, _ := runDispatcher(t, notifier.DispatcherOptions{Clock: clock.Real()})

	d.Enqueue(outage(), []notifier.Notifier{c})

	await(t, "the delivery", func() bool { return c.count() == 1 })
}

func TestDispatcherDeliversToEveryChannel(t *testing.T) {
	a, b := newChannel("discord"), newChannel("slack")
	d, _ := runDispatcher(t, notifier.DispatcherOptions{Clock: clock.Real()})

	d.Enqueue(outage(), []notifier.Notifier{a, b})

	await(t, "both deliveries", func() bool { return a.count() == 1 && b.count() == 1 })
}

// Um canal indisponível não pode impedir que os outros recebam: a queda
// do Slack não deveria calar o alerta do Discord.
func TestFailingChannelDoesNotBlockTheOthers(t *testing.T) {
	quebrado := newChannel("quebrado")
	quebrado.failFor.Store(1000)
	bom := newChannel("bom")

	d, _ := runDispatcher(t, notifier.DispatcherOptions{
		Clock: clock.Real(), MaxAttempts: 1,
	})
	d.Enqueue(outage(), []notifier.Notifier{quebrado, bom})

	await(t, "the healthy channel", func() bool { return bom.count() == 1 })
}

// Falha momentânea é o caso comum; desistir na primeira tentativa
// perderia avisos por um soluço de rede.
func TestDispatcherRetriesTransientFailure(t *testing.T) {
	c := newChannel("instável")
	c.failFor.Store(2)

	d, _ := runDispatcher(t, notifier.DispatcherOptions{
		Clock: clock.Real(), MaxAttempts: 5, RetryDelay: time.Millisecond,
	})
	d.Enqueue(outage(), []notifier.Notifier{c})

	await(t, "the delivery after retries", func() bool { return c.count() == 1 })
	if c.calls.Load() != 3 {
		t.Errorf("attempted %d times, want 3", c.calls.Load())
	}
}

// Retentar para sempre transformaria um destino removido num laço
// infinito consumindo a fila.
func TestDispatcherGivesUpAfterMaxAttempts(t *testing.T) {
	c := newChannel("morto")
	c.failFor.Store(1000)

	d, _ := runDispatcher(t, notifier.DispatcherOptions{
		Clock: clock.Real(), MaxAttempts: 3, RetryDelay: time.Millisecond,
	})
	d.Enqueue(outage(), []notifier.Notifier{c})

	await(t, "the attempts to stop", func() bool { return d.Dropped() > 0 })
	if got := c.calls.Load(); got != 3 {
		t.Errorf("attempted %d times, want it to stop at 3", got)
	}
}

// A propriedade mais importante: quem produz o aviso é o caminho crítico
// do monitoramento, e não pode ficar preso esperando um canal lento.
func TestEnqueueNeverBlocksTheCaller(t *testing.T) {
	lento := newChannel("lento")
	lento.hold = make(chan struct{})
	defer close(lento.hold)

	d, _ := runDispatcher(t, notifier.DispatcherOptions{
		Clock: clock.Real(), QueueSize: 2,
	})

	pronto := make(chan struct{})
	go func() {
		defer close(pronto)
		// Muito mais que o tamanho da fila: se Enqueue bloqueasse, isto
		// travaria aqui.
		for i := 0; i < 200; i++ {
			d.Enqueue(outage(), []notifier.Notifier{lento})
		}
	}()

	select {
	case <-pronto:
	case <-time.After(3 * time.Second):
		t.Fatal("Enqueue blocked the caller; monitoring would stall behind a slow channel")
	}
}

// Perder aviso é ruim, mas travar o monitoramento é pior. O descarte é
// contado para o operador saber que aconteceu.
func TestOverflowIsCountedRatherThanSilent(t *testing.T) {
	lento := newChannel("lento")
	lento.hold = make(chan struct{})
	defer close(lento.hold)

	d, _ := runDispatcher(t, notifier.DispatcherOptions{
		Clock: clock.Real(), QueueSize: 1,
	})

	for i := 0; i < 100; i++ {
		d.Enqueue(outage(), []notifier.Notifier{lento})
	}

	await(t, "the overflow to be counted", func() bool { return d.Dropped() > 0 })
}

// Aviso já aceito precisa sair antes de o processo encerrar: perdê-lo
// apagaria justamente a notícia da queda.
func TestPendingNotificationsAreFlushedOnShutdown(t *testing.T) {
	c := newChannel("discord")
	d, stop := runDispatcher(t, notifier.DispatcherOptions{Clock: clock.Real()})

	d.Enqueue(outage(), []notifier.Notifier{c})
	stop()

	if c.count() != 1 {
		t.Errorf("delivered %d notifications, want the pending one to be flushed", c.count())
	}
}

func TestDispatcherAppliesDefaults(t *testing.T) {
	d := notifier.NewDispatcher(notifier.DispatcherOptions{})

	if d.MaxAttempts() != notifier.DefaultMaxAttempts {
		t.Errorf("MaxAttempts() = %d, want %d", d.MaxAttempts(), notifier.DefaultMaxAttempts)
	}
}

// Sem canal configurado o aviso não tem para onde ir; enfileirá-lo só
// gastaria a fila.
func TestEnqueueWithoutChannelsIsANoop(t *testing.T) {
	d, _ := runDispatcher(t, notifier.DispatcherOptions{Clock: clock.Real()})

	d.Enqueue(outage(), nil)

	if d.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want the empty enqueue to be ignored", d.Dropped())
	}
}
