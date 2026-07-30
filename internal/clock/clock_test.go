package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
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

// Duração zero ou negativa significa "já venceu". O time.Timer real
// dispara de imediato, e um agendador que arme um timer de zero para algo
// vencido ficaria parado para sempre se o relógio falso exigisse Advance.
func TestFakeTimerWithNonPositiveDurationFiresImmediately(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			c := clock.NewFake(epoch)

			timer := c.NewTimer(d)

			select {
			case tick := <-timer.C():
				if !tick.Equal(epoch.Add(d)) {
					t.Errorf("timer delivered %v, want %v", tick, epoch.Add(d))
				}
			default:
				t.Fatal("timer with a non-positive duration did not fire immediately")
			}
		})
	}
}

func TestFakeResetToNonPositiveDurationFiresImmediately(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Hour)

	timer.Reset(0)

	select {
	case <-timer.C():
	default:
		t.Fatal("timer reset to zero did not fire immediately")
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

// Contar timers pendentes não basta para sincronizar com um laço que
// mantém sempre um timer armado: BlockUntil(1) já estaria satisfeito pelo
// timer anterior, e o Advance dispararia antes do rearme. Esperar pelo
// vencimento específico elimina a ambiguidade.
func TestFakeBlockUntilDeadlineWaitsForATimerDueByThatInstant(t *testing.T) {
	c := clock.NewFake(epoch)
	target := epoch.Add(time.Minute)

	// Um timer que vence depois do alvo não satisfaz a espera.
	c.NewTimer(time.Hour)

	released := make(chan struct{})
	go func() {
		c.BlockUntilDeadline(target)
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("BlockUntilDeadline returned while only a later timer was pending")
	case <-time.After(100 * time.Millisecond):
	}

	c.NewTimer(time.Minute)

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("BlockUntilDeadline did not return after a matching timer was armed")
	}
}

func TestFakeBlockUntilDeadlineReturnsImmediatelyWhenSatisfied(t *testing.T) {
	c := clock.NewFake(epoch)
	c.NewTimer(30 * time.Second)

	done := make(chan struct{})
	go func() {
		c.BlockUntilDeadline(epoch.Add(time.Minute))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("BlockUntilDeadline blocked despite an earlier timer already pending")
	}
}

// Reset conta como rearme: é assim que um laço troca o vencimento sem
// alocar timer novo.
func TestFakeBlockUntilDeadlineObservesReset(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(time.Hour)

	released := make(chan struct{})
	go func() {
		c.BlockUntilDeadline(epoch.Add(time.Minute))
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("BlockUntilDeadline returned before the timer was reset")
	case <-time.After(100 * time.Millisecond):
	}

	timer.Reset(time.Minute)

	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("BlockUntilDeadline did not observe the Reset")
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
