package probe_test

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/probe"
)

// countingJob mede sempre o mesmo sinal e conta quantas vezes rodou.
type countingJob struct {
	runs atomic.Int32
	ran  chan string
}

func (c *countingJob) job(name string, every time.Duration, fingerprint string) probe.Job {
	return probe.Job{
		Name: name, PushURL: checkURL, Interval: every, Fingerprint: fingerprint,
		Measure: func(context.Context) probe.Signal {
			c.runs.Add(1)
			c.ran <- name
			return probe.Signal{Status: "up", Msg: name}
		},
	}
}

func newCounting() *countingJob { return &countingJob{ran: make(chan string, 100)} }

func (c *countingJob) expect(t *testing.T, name string) {
	t.Helper()
	select {
	case got := <-c.ran:
		if got != name {
			t.Fatalf("ran %q, want %q", got, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%q did not run", name)
	}
}

func (c *countingJob) quiet(t *testing.T) {
	t.Helper()
	select {
	case got := <-c.ran:
		t.Fatalf("%q ran when nothing was due", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func newRunner(t *testing.T, clk clock.Clock, s *scriptedSend) *probe.Runner {
	t.Helper()
	r := probe.NewRunner(probe.RunnerOptions{Clock: clk, Send: s.send, Backoff: testBackoff})
	t.Cleanup(r.Stop)
	return r
}

// A primeira medição sai na partida, não um intervalo depois: com 60s de
// intervalo, uma sonda recém-instalada ficaria um minuto calada, e o
// monitor push recém-criado no central cairia por silêncio.
func TestRunnerMeasuresAtStartAndEveryInterval(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	c := newCounting()
	r := newRunner(t, clk, s)

	r.Apply([]probe.Job{c.job("rq-workers", time.Minute, "v1")})
	c.expect(t, "rq-workers")
	if got := s.next(t); got.Msg != "rq-workers" {
		t.Errorf("pushed %+v", got)
	}

	clk.BlockUntilDeadline(epoch.Add(time.Minute))
	clk.Advance(59 * time.Second)
	c.quiet(t)
	clk.Advance(time.Second)
	c.expect(t, "rq-workers")

	clk.BlockUntilDeadline(epoch.Add(2 * time.Minute))
	clk.Advance(time.Minute)
	c.expect(t, "rq-workers")
}

// Recarregar a configuração não pode reiniciar a agenda de quem não
// mudou: um add de verificação nova faria todas as outras medirem fora
// de hora, e cada sinal extra conta na confirmação do central.
func TestRunnerApplyKeepsUnchangedJobsAndRestartsChangedOnes(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	c := newCounting()
	r := newRunner(t, clk, s)

	r.Apply([]probe.Job{
		c.job("a", time.Minute, "v1"),
		c.job("b", time.Minute, "v1"),
		c.job("c", time.Minute, "v1"),
	})
	for range 3 {
		<-c.ran
	}

	// a igual, b mudou, c saiu, d entrou.
	r.Apply([]probe.Job{
		c.job("a", time.Minute, "v1"),
		c.job("b", time.Minute, "v2"),
		c.job("d", time.Minute, "v1"),
	})
	got := map[string]bool{}
	for range 2 {
		select {
		case name := <-c.ran:
			got[name] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v ran after Apply", got)
		}
	}
	if !got["b"] || !got["d"] {
		t.Errorf("after Apply ran %v, want the changed b and the new d", got)
	}
	c.quiet(t)

	// No próximo minuto, c não roda mais.
	clk.BlockUntilDeadline(epoch.Add(time.Minute))
	clk.Advance(time.Minute)
	ran := map[string]bool{}
	for range 3 {
		select {
		case name := <-c.ran:
			ran[name] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v ran at the next minute", ran)
		}
	}
	if ran["c"] || !ran["a"] || !ran["b"] || !ran["d"] {
		t.Errorf("at the next minute ran %v, want a, b and d", ran)
	}
}

func TestRunnerStopEndsEveryJob(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	c := newCounting()
	r := probe.NewRunner(probe.RunnerOptions{Clock: clk, Send: s.send, Backoff: testBackoff})

	r.Apply([]probe.Job{c.job("a", time.Minute, "v1")})
	c.expect(t, "a")
	r.Stop()

	clk.Advance(time.Hour)
	c.quiet(t)
}

// O runner avisa cada medição e cada entrega, que é de onde o status
// tira "último resultado" e "último envio".
func TestRunnerReportsResultsAndDeliveries(t *testing.T) {
	clk := clock.NewFake(epoch)
	s := newScriptedSend()
	c := newCounting()

	var mu sync.Mutex
	var results []string
	var sent []probe.Delivery
	r := probe.NewRunner(probe.RunnerOptions{
		Clock: clk, Send: s.send, Backoff: testBackoff,
		OnResult: func(name string, _ time.Time, _ probe.Signal) {
			mu.Lock()
			defer mu.Unlock()
			results = append(results, name)
		},
		OnDelivery: func(name string, d probe.Delivery) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, d)
		},
	})
	t.Cleanup(r.Stop)

	r.Apply([]probe.Job{c.job("a", time.Minute, "v1")})
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(results) == 1 && len(sent) == 1
	})
	if !sent[0].OK {
		t.Errorf("delivery = %+v", sent[0])
	}
}

// Jobs monta uma tarefa por verificação e uma para o sinal de vida, que
// diz o nome da sonda: quando ele some, o aviso do central é sobre a
// sonda, não sobre os serviços.
func TestJobsIncludesTheHeartbeat(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	cfg := probe.Config{
		Name:      "vm-prod-1",
		Heartbeat: probe.Heartbeat{PushURL: heartbeatURL, Interval: probe.Duration(30 * time.Second)},
		Checks: []probe.Check{{
			Name: "porta", Type: "tcp", Target: l.Addr().String(), PushURL: checkURL,
			Interval: probe.Duration(time.Minute), Timeout: probe.Duration(time.Second),
		}},
	}
	jobs := probe.Jobs(cfg, probe.NewRegistry())
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want the check and the heartbeat", len(jobs))
	}

	byName := map[string]probe.Job{}
	for _, j := range jobs {
		byName[j.Name] = j
	}
	hb, ok := byName[probe.HeartbeatName]
	if !ok || hb.PushURL != heartbeatURL || hb.Interval != 30*time.Second {
		t.Fatalf("heartbeat job = %+v", hb)
	}
	if sig := hb.Measure(context.Background()); sig.Status != "up" || sig.Msg != "sonda vm-prod-1 viva" {
		t.Errorf("heartbeat signal = %+v", sig)
	}

	check := byName["porta"]
	if sig := check.Measure(context.Background()); sig.Status != "up" {
		t.Errorf("tcp check signal = %+v, want up", sig)
	}
}
