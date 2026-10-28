package probe

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
)

// HeartbeatName identifica o sinal de vida no status. O @ não cabe em
// nome de verificação, então não há como colidir com uma.
const HeartbeatName = "@sinal-de-vida"

// Job é uma tarefa periódica: medir e mandar o sinal ao push.
type Job struct {
	Name     string
	PushURL  string
	Interval time.Duration
	// Fingerprint muda quando a definição muda. Uma tarefa com o mesmo
	// nome e a mesma impressão segue rodando numa recarga.
	Fingerprint string
	Measure     func(ctx context.Context) Signal
}

// Jobs monta as tarefas da configuração: uma por verificação e o sinal
// de vida, quando configurado.
func Jobs(cfg Config, reg *checker.Registry) []Job {
	jobs := make([]Job, 0, len(cfg.Checks)+1)
	for _, ch := range cfg.Checks {
		m, err := ch.Monitor(monitorID(ch.Name))
		if err != nil {
			// A configuração chega validada; um erro aqui é defeito, e a
			// verificação reporta a causa em vez de sumir em silêncio.
			msg := err.Error()
			jobs = append(jobs, Job{
				Name: ch.Name, PushURL: ch.PushURL, Interval: time.Duration(ch.Interval),
				Fingerprint: fingerprint(ch),
				Measure: func(context.Context) Signal {
					return Signal{Status: "down", Msg: "configuração inválida: " + msg}
				},
			})
			continue
		}
		jobs = append(jobs, Job{
			Name: ch.Name, PushURL: ch.PushURL, Interval: m.Interval,
			Fingerprint: fingerprint(ch),
			Measure: func(ctx context.Context) Signal {
				return SignalFrom(reg.Check(ctx, m))
			},
		})
	}

	if cfg.Heartbeat.PushURL != "" {
		msg := "sonda viva"
		if cfg.Name != "" {
			msg = "sonda " + cfg.Name + " viva"
		}
		jobs = append(jobs, Job{
			Name: HeartbeatName, PushURL: cfg.Heartbeat.PushURL,
			Interval:    time.Duration(cfg.Heartbeat.Interval),
			Fingerprint: cfg.Heartbeat.PushURL + "|" + cfg.Heartbeat.Interval.String() + "|" + cfg.Name,
			Measure: func(context.Context) Signal {
				return Signal{Status: "up", Msg: msg}
			},
		})
	}
	return jobs
}

// monitorID deriva o id do nome. O checker HTTP guarda as asserções
// compiladas por id; derivado do nome, ele não muda quando outra
// verificação sai da lista, e a tarefa que não mudou segue intacta.
func monitorID(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() >> 1)
}

func fingerprint(ch Check) string {
	b, _ := json.Marshal(ch)
	return string(b)
}

// RunnerOptions são as dependências do Runner.
type RunnerOptions struct {
	Clock   clock.Clock
	Send    SendFunc
	Backoff Backoff
	// OnResult recebe cada medição; OnDelivery, cada tentativa de envio.
	OnResult   func(name string, at time.Time, sig Signal)
	OnDelivery func(name string, d Delivery)
}

// Runner mantém uma goroutine por tarefa, cada uma com a própria fila de
// entrega.
//
// Uma fila por endereço, e não uma geral: um push lento ou recusado não
// pode atrasar o sinal de vida, que é justamente o que diz se a sonda
// está de pé.
type Runner struct {
	opts    RunnerOptions
	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	fingerprint string
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewRunner cria o runner, ainda sem tarefas.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.OnResult == nil {
		opts.OnResult = func(string, time.Time, Signal) {}
	}
	if opts.OnDelivery == nil {
		opts.OnDelivery = func(string, Delivery) {}
	}
	return &Runner{opts: opts, workers: map[string]*worker{}}
}

// Apply troca o conjunto de tarefas.
//
// A tarefa que não mudou segue com a agenda que tinha: recarregar a
// configuração para incluir uma verificação não pode fazer as outras
// medirem fora de hora, porque cada sinal extra conta na confirmação do
// central. A que mudou recomeça, e a que saiu para.
func (r *Runner) Apply(jobs []Job) {
	r.mu.Lock()
	defer r.mu.Unlock()

	wanted := make(map[string]Job, len(jobs))
	for _, j := range jobs {
		wanted[j.Name] = j
	}
	for name, w := range r.workers {
		if j, ok := wanted[name]; ok && j.Fingerprint == w.fingerprint {
			continue
		}
		w.stop()
		delete(r.workers, name)
	}
	for _, j := range jobs {
		if _, running := r.workers[j.Name]; running {
			continue
		}
		r.workers[j.Name] = r.start(j)
	}
}

// Stop encerra todas as tarefas e espera que terminem.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, w := range r.workers {
		w.stop()
		delete(r.workers, name)
	}
}

func (w *worker) stop() {
	w.cancel()
	<-w.done
}

func (r *Runner) start(j Job) *worker {
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{fingerprint: j.Fingerprint, cancel: cancel, done: make(chan struct{})}

	out := NewOutbox(r.opts.Clock, j.PushURL, r.opts.Send, r.opts.Backoff,
		func(d Delivery) { r.opts.OnDelivery(j.Name, d) })

	var wg sync.WaitGroup
	wg.Go(func() { out.Run(ctx) })
	wg.Go(func() { r.loop(ctx, j, out) })
	go func() {
		wg.Wait()
		close(w.done)
	}()
	return w
}

// loop mede na partida e depois a cada intervalo.
//
// A próxima medição é marcada a partir do horário previsto, e não do fim
// da anterior: uma verificação que leva 9s de timeout não empurra a
// agenda 9s a cada volta.
func (r *Runner) loop(ctx context.Context, j Job, out *Outbox) {
	next := r.opts.Clock.Now()
	for {
		sig := j.Measure(ctx)
		if ctx.Err() != nil {
			return
		}
		r.opts.OnResult(j.Name, r.opts.Clock.Now(), sig)
		out.Post(sig)

		next = next.Add(j.Interval)
		if now := r.opts.Clock.Now(); next.Before(now) {
			next = now
		}
		t := r.opts.Clock.NewTimer(next.Sub(r.opts.Clock.Now()))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}
