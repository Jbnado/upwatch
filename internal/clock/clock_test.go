package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func TestFakeNowReturnsSeededTime(t *testing.T) {
	c := clock.NewFake(epoch)

	if got := c.Now(); !got.Equal(epoch) {
		t.Errorf("Now() = %v, want %v", got, epoch)
	}
}

func TestFakeAdvanceMovesTimeForward(t *testing.T) {
	c := clock.NewFake(epoch)

	c.Advance(90 * time.Second)

	if want := epoch.Add(90 * time.Second); !c.Now().Equal(want) {
		t.Errorf("Now() = %v, want %v", c.Now(), want)
	}
}

func TestFakeTimerDoesNotFireBeforeDeadline(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)

	c.Advance(59 * time.Second)

	select {
	case tick := <-timer.C():
		t.Fatalf("timer fired early at %v", tick)
	default:
	}
}

// Semântica de >=: um timer de 1min agendado para T+60s dispara quando o
// relógio chega exatamente em T+60s, igual ao time.Timer real.
func TestFakeTimerFiresExactlyAtDeadline(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)

	c.Advance(time.Minute)

	select {
	case tick := <-timer.C():
		if want := epoch.Add(time.Minute); !tick.Equal(want) {
			t.Errorf("timer delivered %v, want %v", tick, want)
		}
	default:
		t.Fatal("timer did not fire at its deadline")
	}
}

// O valor entregue é o instante do vencimento, não o "agora" após o salto:
// o scheduler usa esse valor para calcular o próximo vencimento e um salto
// grande causaria deriva acumulada.
func TestFakeTimerDeliversDeadlineNotCurrentTime(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)

	c.Advance(10 * time.Minute)

	tick := <-timer.C()
	if want := epoch.Add(time.Minute); !tick.Equal(want) {
		t.Errorf("timer delivered %v, want %v", tick, want)
	}
}

func TestFakeStopPreventsFiring(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)

	if !timer.Stop() {
		t.Error("Stop() = false on a pending timer, want true")
	}
	c.Advance(time.Hour)

	select {
	case <-timer.C():
		t.Fatal("stopped timer fired")
	default:
	}
}

func TestFakeStopReturnsFalseWhenAlreadyFired(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)
	c.Advance(time.Minute)

	if timer.Stop() {
		t.Error("Stop() = true on an already-fired timer, want false")
	}
}

// O scheduler reagenda o mesmo timer a cada volta do laço em vez de alocar
// um novo; sem Reset funcionando, cada monitor vazaria um timer por check.
func TestFakeResetReschedulesTimer(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Minute)

	timer.Reset(2 * time.Minute)
	c.Advance(time.Minute)

	select {
	case <-timer.C():
		t.Fatal("timer fired at the original deadline after Reset")
	default:
	}

	c.Advance(time.Minute)

	select {
	case <-timer.C():
	default:
		t.Fatal("timer did not fire at the new deadline")
	}
}

func TestFakeAdvanceFiresAllElapsedTimers(t *testing.T) {
	c := clock.NewFake(epoch)
	first := c.NewTimer(time.Minute)
	second := c.NewTimer(2 * time.Minute)
	third := c.NewTimer(time.Hour)

	c.Advance(5 * time.Minute)

	for name, timer := range map[string]clock.Timer{"first": first, "second": second} {
		select {
		case <-timer.C():
		default:
			t.Errorf("%s timer did not fire", name)
		}
	}
	select {
	case <-third.C():
		t.Error("third timer fired before its deadline")
	default:
	}
}

func TestFakeAfterFiresLikeTimer(t *testing.T) {
	c := clock.NewFake(epoch)
	ch := c.After(time.Minute)

	c.Advance(time.Minute)

	select {
	case tick := <-ch:
		if want := epoch.Add(time.Minute); !tick.Equal(want) {
			t.Errorf("After delivered %v, want %v", tick, want)
		}
	default:
		t.Fatal("After channel did not fire")
	}
}

// Sem BlockUntil, um teste que dá Advance antes de a goroutine sob teste
// ter registrado seu timer passa a depender de escalonamento — é a origem
// clássica de teste instável com relógio falso.
func TestFakeBlockUntilWaitsForWaiters(t *testing.T) {
	c := clock.NewFake(epoch)
	fired := make(chan time.Time, 1)

	go func() {
		fired <- <-c.After(time.Minute)
	}()

	c.BlockUntil(1)
	c.Advance(time.Minute)

	select {
	case tick := <-fired:
		if want := epoch.Add(time.Minute); !tick.Equal(want) {
			t.Errorf("goroutine saw %v, want %v", tick, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never observed the timer firing")
	}
}

func TestFakeBlockUntilReturnsImmediatelyWhenAlreadySatisfied(t *testing.T) {
	c := clock.NewFake(epoch)
	c.NewTimer(time.Minute)
	c.NewTimer(time.Minute)

	done := make(chan struct{})
	go func() {
		c.BlockUntil(2)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("BlockUntil blocked despite the waiter count already being met")
	}
}

// O scheduler mexe no relógio a partir de várias goroutines; uma corrida
// aqui contaminaria todo teste que dependa de tempo.
func TestFakeIsSafeForConcurrentUse(t *testing.T) {
	c := clock.NewFake(epoch)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			timer := c.NewTimer(time.Millisecond)
			timer.Stop()
		}()
		go func() {
			defer wg.Done()
			_ = c.Now()
			c.Advance(time.Millisecond)
		}()
	}

	wg.Wait()
}

func TestRealClockNowTracksWallTime(t *testing.T) {
	c := clock.Real()

	before := time.Now()
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want between %v and %v", got, before, after)
	}
}

func TestRealClockTimerFires(t *testing.T) {
	c := clock.Real()
	timer := c.NewTimer(10 * time.Millisecond)

	select {
	case <-timer.C():
	case <-time.After(2 * time.Second):
		t.Fatal("real timer did not fire")
	}
}

func TestRealClockStopPreventsFiring(t *testing.T) {
	c := clock.Real()
	timer := c.NewTimer(time.Hour)

	if !timer.Stop() {
		t.Error("Stop() = false on a pending timer, want true")
	}
}
