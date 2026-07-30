package metrics_test

import (
	"sync"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/metrics"
)

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
