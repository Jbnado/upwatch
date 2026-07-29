package rollup

import (
	"context"
	"fmt"
	"time"

	"github.com/bernardojoao/upwatch/internal/clock"
	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// DefaultInterval é a frequência de execução do ciclo. Buckets horários
// fecham a cada hora, então checar de cinco em cinco minutos é folgado o
// bastante para nada atrasar e barato o suficiente para não pesar.
const DefaultInterval = 5 * time.Minute

// DefaultRetention é a cascata padrão.
//
// Com cem monitores a cada minuto, isso estabiliza o banco perto de cem
// megabytes em vez de crescer indefinidamente: o dado cru cobre a semana
// de investigação, o horário cobre o trimestre de análise e o diário
// sustenta o histórico longo de SLA.
var DefaultRetention = Retention{
	Raw:    7 * 24 * time.Hour,
	Hourly: 90 * 24 * time.Hour,
	Daily:  730 * 24 * time.Hour,
}

// resolutions é a ordem de processamento das camadas.
var resolutions = []domain.Resolution{domain.ResolutionHourly, domain.ResolutionDaily}

// Retention é por quanto tempo cada camada sobrevive.
type Retention struct {
	Raw    time.Duration
	Hourly time.Duration
	Daily  time.Duration
}

// Validate confere que a cascata é coerente.
//
// Reter o agregado por menos tempo que o dado cru inverteria a ideia: o
// detalhe sobreviveria ao resumo que deveria substituí-lo, e o gráfico
// longo ficaria com buracos onde o dado ainda existe.
func (r Retention) Validate() error {
	if r.Raw <= 0 || r.Hourly <= 0 || r.Daily <= 0 {
		return fmt.Errorf("rollup: toda janela de retenção precisa ser positiva")
	}
	if r.Hourly < r.Raw {
		return fmt.Errorf("rollup: retenção horária (%s) menor que a do dado cru (%s)", r.Hourly, r.Raw)
	}
	if r.Daily < r.Hourly {
		return fmt.Errorf("rollup: retenção diária (%s) menor que a horária (%s)", r.Daily, r.Hourly)
	}
	return nil
}

// forResolution devolve a janela da camada.
func (r Retention) forResolution(res domain.Resolution) time.Duration {
	if res == domain.ResolutionDaily {
		return r.Daily
	}
	return r.Hourly
}

// Options configura o worker. Campos zerados assumem o padrão.
type Options struct {
	Interval  time.Duration
	Retention Retention
	Clock     clock.Clock
}

// Worker agrega batidas em estatísticas e aplica a retenção.
type Worker struct {
	store store.Store
	opts  Options
}

// NewWorker cria o worker. É preciso chamar Run ou RunOnce para ele operar.
func NewWorker(s store.Store, opts Options) *Worker {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Retention.Validate() != nil {
		opts.Retention = DefaultRetention
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	return &Worker{store: s, opts: opts}
}

// Interval é a frequência do ciclo.
func (w *Worker) Interval() time.Duration { return w.opts.Interval }

// Retention é a cascata configurada.
func (w *Worker) Retention() Retention { return w.opts.Retention }

// Run executa ciclos até o contexto terminar.
func (w *Worker) Run(ctx context.Context) {
	// Primeiro ciclo imediato: após um reinício pode haver muita janela
	// pendente, e esperar o intervalo inteiro só atrasaria a recuperação.
	if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
		// Um ciclo com falha não pode encerrar o worker: a próxima volta
		// reprocessa, já que a marca d'água não avançou.
		_ = err
	}

	timer := w.opts.Clock.NewTimer(w.opts.Interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C():
			_ = w.RunOnce(ctx)
			timer.Reset(w.opts.Interval)
		}
	}
}

// RunOnce executa um ciclo: agrega e só então poda.
//
// A ordem é a invariante mais cara de violar do projeto. Podar antes de
// agregar apagaria batidas que nunca viraram estatística — perda
// definitiva, sem qualquer forma de recuperação.
func (w *Worker) RunOnce(ctx context.Context) error {
	if err := w.aggregate(ctx); err != nil {
		return err
	}
	return w.prune(ctx)
}

// aggregate processa os buckets encerrados de cada resolução.
func (w *Worker) aggregate(ctx context.Context) error {
	monitors, err := w.allMonitors(ctx)
	if err != nil {
		return err
	}
	if len(monitors) == 0 {
		return nil
	}

	now := w.opts.Clock.Now().UTC()

	for _, res := range resolutions {
		if err := w.aggregateResolution(ctx, res, monitors, now); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) aggregateResolution(
	ctx context.Context,
	res domain.Resolution,
	monitors []domain.Monitor,
	now time.Time,
) error {
	mark, err := w.store.RollupWatermark(ctx, res)
	if err != nil {
		return err
	}

	start, ok, err := w.startBucket(ctx, res, mark, now)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	// O bucket corrente fica de fora: agregá-lo gravaria estatística
	// parcial como definitiva, e ela nunca seria corrigida.
	limit := res.Truncate(now)

	var processed time.Time
	for bucketStart := start; bucketStart.Before(limit); bucketStart = bucketStart.Add(res.Duration()) {
		if err := w.aggregateBucket(ctx, res, bucketStart, monitors); err != nil {
			return err
		}
		processed = bucketStart
	}

	if processed.IsZero() {
		return nil
	}
	return w.store.SetRollupWatermark(ctx, res, processed)
}

// startBucket decide onde retomar, e se há o que processar.
//
// Sem marca d'água, parte da batida mais antiga que existe. Partir da
// janela de retenção seria errado justamente no primeiro ciclo: a poda só
// roda depois da agregação, então há dado mais velho que a janela ainda
// presente, e ignorá-lo o apagaria sem que jamais virasse estatística.
func (w *Worker) startBucket(
	ctx context.Context,
	res domain.Resolution,
	mark, now time.Time,
) (time.Time, bool, error) {
	if !mark.IsZero() {
		return res.Truncate(mark).Add(res.Duration()), true, nil
	}

	oldest, ok, err := w.store.OldestHeartbeat(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	if !ok {
		return time.Time{}, false, nil
	}
	return res.Truncate(oldest), true, nil
}

// aggregateBucket resume um bucket para todos os monitores.
func (w *Worker) aggregateBucket(
	ctx context.Context,
	res domain.Resolution,
	bucketStart time.Time,
	monitors []domain.Monitor,
) error {
	window := store.TimeRange{From: bucketStart, To: bucketStart.Add(res.Duration())}

	var batch []domain.Rollup
	for _, m := range monitors {
		// Agrupado por probe: hoje só existe a instância local, mas
		// separar desde já evita misturar origens quando probes remotos
		// chegarem, sem custo algum agora.
		byProbe := map[string][]domain.Heartbeat{}

		err := w.store.StreamHeartbeats(ctx, m.ID, window, func(hb domain.Heartbeat) error {
			byProbe[hb.ProbeID] = append(byProbe[hb.ProbeID], hb)
			return nil
		})
		if err != nil {
			return fmt.Errorf("rollup: lendo batidas do monitor %d: %w", m.ID, err)
		}

		for probeID, hbs := range byProbe {
			// Bucket sem amostra não vira linha: um agregado vazio só
			// infla a tabela e apareceria como zero por cento de uptime.
			if len(hbs) == 0 {
				continue
			}
			batch = append(batch, Aggregate(Bucket{
				MonitorID:  m.ID,
				ProbeID:    probeID,
				Resolution: res,
				Start:      bucketStart,
			}, hbs))
		}
	}

	if len(batch) == 0 {
		return nil
	}
	if err := w.store.WriteRollups(ctx, batch); err != nil {
		return fmt.Errorf("rollup: gravando agregados de %s: %w", bucketStart, err)
	}
	return nil
}

// prune aplica a retenção de cada camada e devolve o espaço liberado.
func (w *Worker) prune(ctx context.Context) error {
	now := w.opts.Clock.Now().UTC()

	removed, err := w.store.PruneHeartbeats(ctx, now.Add(-w.opts.Retention.Raw))
	if err != nil {
		return fmt.Errorf("rollup: podando batidas: %w", err)
	}

	for _, res := range resolutions {
		cutoff := now.Add(-w.opts.Retention.forResolution(res))
		n, err := w.store.PruneRollups(ctx, res, cutoff)
		if err != nil {
			return fmt.Errorf("rollup: podando agregados %s: %w", res, err)
		}
		removed += n
	}

	// Só compacta quando houve poda. Apagar linhas não encolhe o arquivo
	// sozinho, mas rodar a recuperação em todo ciclo gastaria I/O sem
	// nada a devolver.
	if removed == 0 {
		return nil
	}
	if err := w.store.Compact(ctx); err != nil {
		return fmt.Errorf("rollup: recuperando espaço: %w", err)
	}
	return nil
}

// allMonitors lista todos os monitores, paginando.
//
// Inclui os pausados: eles têm histórico anterior à pausa que ainda
// precisa ser agregado antes de o dado cru expirar.
func (w *Worker) allMonitors(ctx context.Context) ([]domain.Monitor, error) {
	var (
		out    []domain.Monitor
		filter = store.MonitorFilter{Page: store.PageFilter{Limit: store.MaxPageSize}}
	)

	for {
		page, err := w.store.Monitors().List(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("rollup: listando monitores: %w", err)
		}
		out = append(out, page.Items...)

		if !page.HasMore || len(page.Items) == 0 {
			return out, nil
		}
		filter.Page.AfterID = page.Items[len(page.Items)-1].ID
	}
}
