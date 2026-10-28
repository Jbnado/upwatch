package probe_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/probe"
)

var epoch = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// scriptedSend entrega cada tentativa no canal e devolve o erro da vez.
type scriptedSend struct {
	mu       sync.Mutex
	errs     []error
	calls    chan probe.Signal
	fallback error
}

func newScriptedSend(errs ...error) *scriptedSend {
	return &scriptedSend{errs: errs, calls: make(chan probe.Signal, 100)}
}

func (s *scriptedSend) send(ctx context.Context, _ string, sig probe.Signal) error {
	s.mu.Lock()
	var err error
	if len(s.errs) > 0 {
		err, s.errs = s.errs[0], s.errs[1:]
	} else {
		err = s.fallback
	}
	s.mu.Unlock()
	// Como o cliente HTTP de verdade, desiste quando o contexto acaba: sem
	// isso, um teste que não lê as tentativas travaria a parada do runner.
	select {
	case s.calls <- sig:
	case <-ctx.Done():
		return ctx.Err()
	}
	return err
}

func (s *scriptedSend) next(t *testing.T) probe.Signal {
	t.Helper()
	select {
	case sig := <-s.calls:
		return sig
	case <-time.After(5 * time.Second):
		t.Fatal("no send attempt happened")
		return probe.Signal{}
	}
}

func (s *scriptedSend) none(t *testing.T) {
	t.Helper()
	select {
	case sig := <-s.calls:
		t.Fatalf("unexpected send attempt: %+v", sig)
	case <-time.After(100 * time.Millisecond):
	}
}

var retryable = &probe.SendError{Retryable: true, Msg: "o central respondeu 503", HTTPStatus: 503}

type deliveries struct {
	mu  sync.Mutex
	all []probe.Delivery
}

func (d *deliveries) report(x probe.Delivery) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.all = append(d.all, x)
}

func (d *deliveries) last() probe.Delivery {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.all) == 0 {
		return probe.Delivery{}
	}
	return d.all[len(d.all)-1]
}

func startOutbox(t *testing.T, clk clock.Clock, s *scriptedSend, b probe.Backoff) (*probe.Outbox, *deliveries) {
	t.Helper()
	d := &deliveries{}
	o := probe.NewOutbox(clk, checkURL, s.send, b, d.report)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return o, d
}

var testBackoff = probe.Backoff{Initial: time.Second, Max: 4 * time.Second, Attempts: 4}

func TestBackoffDoublesUpToTheCeiling(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, w := range want {
		if got := testBackoff.Delay(i + 1); got != w {
			t.Errorf("Delay(%d) = %s, want %s", i+1, got, w)
		}
	}
}

// Um soluço do central (reinício, deploy) não pode virar sinal perdido:
// o próximo só sairia um intervalo depois, e com N=1 o monitor cairia
// por silêncio sem nada ter caído.
func TestOutboxRetriesWithBackoff(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend(retryable, retryable, nil)
	o, d := startOutbox(t, clk, s, testBackoff)

	o.Post(probe.Signal{Status: "up"})
	s.next(t)

	clk.BlockUntilDeadline(epoch.Add(time.Second))
	clk.Advance(999 * time.Millisecond)
	s.none(t)
	clk.Advance(time.Millisecond)
	s.next(t)

	clk.BlockUntilDeadline(clk.Now().Add(2 * time.Second))
	clk.Advance(2 * time.Second)
	s.next(t)

	waitFor(t, func() bool { return d.last().OK })
	if got := d.last(); got.Attempts != 3 {
		t.Errorf("delivery = %+v, want OK on the third attempt", got)
	}
}

// Só o último resultado importa. Durante uma queda longa do central, uma
// fila de todos os resultados cresceria sem limite e, na volta, faria o
// central receber meia hora de sinais velhos em rajada — contando cada
// um na confirmação.
func TestOutboxReplacesThePendingSignalWithTheNewest(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	s.fallback = retryable
	o, _ := startOutbox(t, clk, s, testBackoff)

	o.Post(probe.Signal{Status: "up", Msg: "velho"})
	s.next(t)
	clk.BlockUntil(1)

	o.Post(probe.Signal{Status: "down", Msg: "novo"})
	if got := s.next(t); got.Msg != "novo" {
		t.Fatalf("sent %+v, want the newest signal", got)
	}

	// O velho não volta numa nova tentativa.
	for range testBackoff.Attempts - 1 {
		clk.BlockUntil(1)
		clk.Advance(testBackoff.Max)
		if got := s.next(t); got.Msg != "novo" {
			t.Fatalf("retried %+v, want only the newest signal", got)
		}
	}
	clk.Advance(time.Hour)
	s.none(t)
}

// Com o central fora por muito tempo, a sonda desiste depois de algumas
// tentativas e espera o próximo resultado: não acumula fila nem martela
// o central.
func TestOutboxGivesUpAfterTheLastAttempt(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	s.fallback = retryable
	o, d := startOutbox(t, clk, s, testBackoff)

	o.Post(probe.Signal{Status: "up"})
	s.next(t)
	for range testBackoff.Attempts - 1 {
		clk.BlockUntil(1)
		clk.Advance(testBackoff.Max)
		s.next(t)
	}
	clk.Advance(time.Hour)
	s.none(t)

	waitFor(t, func() bool { return d.last().GaveUp })
	if got := d.last(); got.OK || got.Attempts != testBackoff.Attempts || got.HTTPStatus != 503 {
		t.Errorf("last delivery = %+v", got)
	}
}

// Token errado não melhora com insistência: repetir só gastaria o
// limite de taxa do central, que é por origem e dividido com as outras
// verificações da sonda.
func TestOutboxDoesNotRetryPermanentFailures(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend(&probe.SendError{HTTPStatus: 401, Msg: "token"})
	o, d := startOutbox(t, clk, s, testBackoff)

	o.Post(probe.Signal{Status: "up"})
	s.next(t)
	clk.Advance(time.Hour)
	s.none(t)
	waitFor(t, func() bool { return d.last().GaveUp })
}

func TestOutboxHonorsRetryAfter(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend(&probe.SendError{Retryable: true, HTTPStatus: 429, RetryAfter: 10 * time.Second}, nil)
	o, _ := startOutbox(t, clk, s, testBackoff)

	o.Post(probe.Signal{Status: "up"})
	s.next(t)
	clk.BlockUntil(1)
	clk.Advance(9 * time.Second)
	s.none(t)
	clk.Advance(time.Second)
	s.next(t)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
