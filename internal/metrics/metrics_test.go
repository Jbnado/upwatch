package metrics_test

import (
	"sync"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/metrics"
)

func TestSchedulerMetricsTrackWaitingRunningAndSamples(t *testing.T) {
	c := metrics.New()
	c.QueueSchedulerChecks(2)
	if got := c.SchedulerSnapshot(); got.Waiting != 2 || got.Running != 0 {
		t.Fatalf("antes do início: espera=%d execução=%d, queria 2 e 0", got.Waiting, got.Running)
	}

	c.StartSchedulerCheck(250 * time.Millisecond)
	got := c.SchedulerSnapshot()
	if got.Waiting != 1 || got.Running != 1 {
		t.Errorf("após início: espera=%d execução=%d, queria 1 e 1", got.Waiting, got.Running)
	}
	if got.StartDelay.Count != 1 || got.StartDelay.SumSeconds != 0.25 {
		t.Errorf("atraso = (%d, %g), queria (1, 0.25)", got.StartDelay.Count, got.StartDelay.SumSeconds)
	}
	if got.StartDelay.Buckets[0].Count != 0 || got.StartDelay.Buckets[1].Count != 1 {
		t.Errorf("buckets de 0,1s e 0,5s = %d e %d, queria 0 e 1", got.StartDelay.Buckets[0].Count, got.StartDelay.Buckets[1].Count)
	}

	c.FinishSchedulerCheck(2 * time.Second)
	c.UnqueueSchedulerChecks(1)
	got = c.SchedulerSnapshot()
	if got.Waiting != 0 || got.Running != 0 {
		t.Errorf("ao terminar: espera=%d execução=%d, queria zero", got.Waiting, got.Running)
	}
	if got.CheckDuration.Count != 1 || got.CheckDuration.SumSeconds != 2 {
		t.Errorf("duração = (%d, %g), queria (1, 2)", got.CheckDuration.Count, got.CheckDuration.SumSeconds)
	}
	if got.CheckDuration.Buckets[2].Count != 0 || got.CheckDuration.Buckets[3].Count != 1 {
		t.Errorf("buckets de 1s e 5s = %d e %d, queria 0 e 1", got.CheckDuration.Buckets[2].Count, got.CheckDuration.Buckets[3].Count)
	}
}

func TestSchedulerMetricsClampNegativeTimeAndAcceptNil(t *testing.T) {
	var empty *metrics.Counters
	empty.QueueSchedulerChecks(1)
	empty.StartSchedulerCheck(time.Second)
	empty.FinishSchedulerCheck(time.Second)
	empty.UnqueueSchedulerChecks(1)
	if got := empty.SchedulerSnapshot(); got.Waiting != 0 || got.Running != 0 {
		t.Fatalf("snapshot nulo = %+v, queria zero", got)
	}

	c := metrics.New()
	c.QueueSchedulerChecks(1)
	c.StartSchedulerCheck(-time.Second)
	c.FinishSchedulerCheck(-2 * time.Second)
	got := c.SchedulerSnapshot()
	if got.StartDelay.SumSeconds != 0 || got.CheckDuration.SumSeconds != 0 {
		t.Errorf("tempo negativo entrou na soma: atraso=%g duração=%g", got.StartDelay.SumSeconds, got.CheckDuration.SumSeconds)
	}
	if got.StartDelay.Buckets[0].Count != 1 || got.CheckDuration.Buckets[0].Count != 1 {
		t.Error("tempo negativo deveria cair no bucket de até 0,1 segundo")
	}
}

func TestSchedulerMetricsSnapshotConcurrentWithChecks(t *testing.T) {
	c := metrics.New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.QueueSchedulerChecks(1)
				c.StartSchedulerCheck(time.Millisecond)
				c.FinishSchedulerCheck(time.Millisecond)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		got := c.SchedulerSnapshot()
		if got.Waiting < 0 || got.Running < 0 {
			t.Fatalf("gauges negativos durante a coleta: %+v", got)
		}
	}
	wg.Wait()
	got := c.SchedulerSnapshot()
	if got.Waiting != 0 || got.Running != 0 || got.StartDelay.Count != 2000 || got.CheckDuration.Count != 2000 {
		t.Errorf("resultado final = %+v, queria gauges zero e 2000 amostras", got)
	}
}

func TestCountersSeparamOsEstados(t *testing.T) {
	c := metrics.New()

	c.ObserveCheck(domain.StatusUp)
	c.ObserveCheck(domain.StatusUp)
	c.ObserveCheck(domain.StatusDown)

	if got := c.Checks(domain.StatusUp); got != 2 {
		t.Errorf("no ar: esperava 2, veio %d", got)
	}
	if got := c.Checks(domain.StatusDown); got != 1 {
		t.Errorf("fora do ar: esperava 1, veio %d", got)
	}
	if got := c.Checks(domain.StatusDegraded); got != 0 {
		t.Errorf("degradado deveria estar zerado, veio %d", got)
	}
}

func TestCountersSeparamEntregueDeDescartado(t *testing.T) {
	// Durante um incidente a pergunta é se o aviso saiu; um número só
	// somando os dois não responde isso.
	c := metrics.New()

	c.ObserveNotification(true)
	c.ObserveNotification(false)
	c.ObserveNotification(false)

	if got := c.NotificationsSent(); got != 1 {
		t.Errorf("entregues: esperava 1, veio %d", got)
	}
	if got := c.NotificationsDropped(); got != 2 {
		t.Errorf("descartados: esperava 2, veio %d", got)
	}
}

func TestCountersNuloNaoExplode(t *testing.T) {
	// O contador é opcional: um teste que monta a API sem ele não deveria
	// precisar saber que ele existe.
	var c *metrics.Counters

	c.ObserveCheck(domain.StatusUp)
	c.ObserveNotification(true)

	if got := c.Checks(domain.StatusUp); got != 0 {
		t.Errorf("contador nulo devolveu %d", got)
	}
}

func TestCountersSaoSegurosEmParalelo(t *testing.T) {
	// O agendador escreve de várias goroutines enquanto a raspagem lê.
	c := metrics.New()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.ObserveCheck(domain.StatusUp)
			}
		}()
	}
	wg.Wait()

	if got := c.Checks(domain.StatusUp); got != 5000 {
		t.Fatalf("esperava 5000, veio %d", got)
	}
}
