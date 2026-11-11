package scheduler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/metrics"
	"github.com/Jbnado/upwatch/internal/scheduler"
)

// capacityProbe simula três perfis sem abrir conexões. O experimento mede
// a fila do agendador; banco, rede e destino real devem ser medidos à parte.
type capacityProbe struct {
	scheduler *scheduler.Scheduler
	counters  *metrics.Counters
	mu        sync.Mutex
	delays    map[int64][]time.Duration
	peak      atomic.Int64
}

func (p *capacityProbe) Type() domain.MonitorType             { return domain.MonitorHTTP }
func (p *capacityProbe) ValidateConfig(json.RawMessage) error { return nil }

func (p *capacityProbe) Check(ctx context.Context, m domain.Monitor) checker.Result {
	due, _ := p.scheduler.NextRun(m.ID)
	delay := time.Since(due)
	if delay < 0 {
		delay = 0
	}
	p.mu.Lock()
	p.delays[*m.GroupID] = append(p.delays[*m.GroupID], delay)
	p.mu.Unlock()

	waiting := p.scheduler.WaitingChecks()
	for {
		peak := p.peak.Load()
		if waiting <= peak || p.peak.CompareAndSwap(peak, waiting) {
			break
		}
	}

	var work time.Duration
	switch *m.GroupID {
	case 1, 4: // checks lentos, usados também na comparação entre grupos
		work = 25 * time.Millisecond
	case 2: // poucos checks rápidos
		work = time.Millisecond
	case 3: // destino que sempre atinge o timeout
		<-ctx.Done()
		return checker.Result{Status: domain.StatusDown}
	}
	timer := time.NewTimer(work)
	defer timer.Stop()
	select {
	case <-timer.C:
		return checker.Result{Status: domain.StatusUp}
	case <-ctx.Done():
		return checker.Result{Status: domain.StatusDown}
	}
}

func maximumMillis(values []time.Duration) float64 {
	var maximum time.Duration
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return float64(maximum) / float64(time.Millisecond)
}

// BenchmarkSchedulerFairness mantém perfil e intervalo iguais nos dois
// grupos, mas dá ao primeiro quatro vezes mais monitores. O p95 e o
// máximo mostram se o grupo menor acumula atraso desproporcional.
func BenchmarkSchedulerFairness(b *testing.B) {
	b.ReportAllocs()
	all := map[int64][]time.Duration{1: {}, 4: {}}
	var startsLarge, startsSmall int
	b.ResetTimer()
	for range b.N {
		p := &capacityProbe{delays: map[int64][]time.Duration{1: {}, 4: {}}}
		reg, err := checker.NewRegistry(p)
		if err != nil {
			b.Fatal(err)
		}
		counters := metrics.New()
		sch := scheduler.New(reg, capacitySink{}, scheduler.Options{
			Workers: 8, DisableJitter: true, Counters: counters,
		})
		p.scheduler, p.counters = sch, counters
		for _, group := range []struct {
			id    int64
			count int
		}{{1, 80}, {4, 20}} {
			for i := 0; i < group.count; i++ {
				m := monitor(group.id*1000+int64(i), 100*time.Millisecond)
				m.GroupID = &group.id
				m.Timeout = 50 * time.Millisecond
				sch.Upsert(m)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		sch.Run(ctx)
		cancel()
		all[1] = append(all[1], p.delays[1]...)
		all[4] = append(all[4], p.delays[4]...)
		startsLarge += len(p.delays[1])
		startsSmall += len(p.delays[4])
	}
	b.StopTimer()
	b.ReportMetric(percentile95(all[1]), "large_p95_ms")
	b.ReportMetric(percentile95(all[4]), "small_p95_ms")
	b.ReportMetric(maximumMillis(all[1]), "large_max_ms")
	b.ReportMetric(maximumMillis(all[4]), "small_max_ms")
	b.ReportMetric(float64(startsLarge)/float64(b.N), "large_starts/op")
	b.ReportMetric(float64(startsSmall)/float64(b.N), "small_starts/op")
}

type capacitySink struct{}

func (capacitySink) Submit(context.Context, domain.Heartbeat) error { return nil }

func percentile95(values []time.Duration) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (95*len(values)+99)/100 - 1
	return float64(values[index]) / float64(time.Millisecond)
}

// BenchmarkSchedulerCapacity compara grupos sob a mesma carga com dois
// tamanhos de pool. Execute com -benchtime=2s -benchmem; os números são
// caracterização da máquina do teste, nunca um limite universal.
func BenchmarkSchedulerCapacity(b *testing.B) {
	for _, workers := range []int{8, 32} {
		b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			all := map[int64][]time.Duration{1: {}, 2: {}, 3: {}}
			var peak int64
			var checks int64
			b.ResetTimer()
			for range b.N {
				p := &capacityProbe{delays: map[int64][]time.Duration{1: {}, 2: {}, 3: {}}}
				reg, err := checker.NewRegistry(p)
				if err != nil {
					b.Fatal(err)
				}
				counters := metrics.New()
				sch := scheduler.New(reg, capacitySink{}, scheduler.Options{
					Workers: workers, DisableJitter: true, Counters: counters,
				})
				p.scheduler, p.counters = sch, counters
				for _, group := range []struct {
					id       int64
					count    int
					interval time.Duration
					timeout  time.Duration
				}{
					{1, 60, 60 * time.Millisecond, 40 * time.Millisecond},
					{2, 20, 100 * time.Millisecond, 15 * time.Millisecond},
					{3, 20, 80 * time.Millisecond, 8 * time.Millisecond},
				} {
					for i := 0; i < group.count; i++ {
						m := monitor(group.id*1000+int64(i), group.interval)
						m.GroupID = &group.id
						m.Timeout = group.timeout
						sch.Upsert(m)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				sch.Run(ctx)
				cancel()
				for group, samples := range p.delays {
					all[group] = append(all[group], samples...)
				}
				if p.peak.Load() > peak {
					peak = p.peak.Load()
				}
				checks += counters.SchedulerSnapshot().StartDelay.Count
			}
			b.StopTimer()
			b.ReportMetric(percentile95(all[1]), "slow_p95_ms")
			b.ReportMetric(percentile95(all[2]), "fast_p95_ms")
			b.ReportMetric(percentile95(all[3]), "timeout_p95_ms")
			b.ReportMetric(float64(peak), "waiting_peak")
			b.ReportMetric(float64(checks)/float64(b.N), "starts/op")
		})
	}
}
