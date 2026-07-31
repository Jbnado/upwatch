package api

import (
	"net/http"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/summary"
)

// defaultBuckets é a resolução da faixa desenhada.
//
// A faixa do painel tem menos de duzentos pixels; pedir mais buckets que
// isso gasta banda para desenhar traços de menos de um pixel.
const defaultBuckets = 60

// maxBuckets impede que um cliente peça uma série arbitrariamente grande.
const maxBuckets = 500

// summaryResponse é o resumo de uma janela para um monitor.
//
// Os campos numéricos são ponteiros porque ausência de medição é ausência,
// nunca zero: zero afirma "esteve fora o tempo todo" ou "respondeu
// instantaneamente", e afirmar isso sobre o que não se mediu é a forma
// mais silenciosa de uma ferramenta de vigilância mentir.
type summaryResponse struct {
	MonitorID     int64      `json:"monitor_id"`
	Status        string     `json:"status"`
	Source        string     `json:"source"`
	UptimePercent *float64   `json:"uptime_percent"`
	LatencyP50MS  *float64   `json:"latency_p50_ms"`
	LatencyP95MS  *float64   `json:"latency_p95_ms"`
	LatencyP99MS  *float64   `json:"latency_p99_ms"`
	LastCheckAt   *time.Time `json:"last_check_at"`
	Up            int        `json:"up"`
	Degraded      int        `json:"degraded"`
	Down          int        `json:"down"`
	Unknown       int        `json:"unknown"`
	Series        []point    `json:"series"`
}

type point struct {
	At        time.Time `json:"at"`
	Status    string    `json:"status"`
	LatencyMS *float64  `json:"latency_ms"`
}

// handleSummaries devolve o resumo da janela para todos os monitores.
//
// Existe para que a interface não calcule nada. Antes, painel e tela de
// detalhe somavam disponibilidade e percentis cada um por conta própria,
// em cópias separadas do mesmo cálculo — e discordavam sobre o que fazer
// com ausência de medição sem que nada acusasse. Agora o número é do
// servidor, e quem consulta por script recebe exatamente o que a tela
// mostra.
//
// Uma requisição para todos os monitores, e não uma por monitor: o painel
// abria N conexões para desenhar uma lista, o que degradava exatamente na
// instalação grande, onde a lista importa mais.
func (a *API) handleSummaries(w http.ResponseWriter, r *http.Request) {
	rng, err := a.timeRange(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	buckets := int(queryInt64(r, "buckets", defaultBuckets))
	if buckets <= 0 {
		buckets = defaultBuckets
	}
	if buckets > maxBuckets {
		buckets = maxBuckets
	}

	page, err := a.store.Monitors().List(r.Context(), store.MonitorFilter{
		Page: store.PageFilter{Limit: store.MaxPageSize},
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// A camada é escolhida aqui, e não por quem chama: janela curta lê
	// batidas cruas, janela longa lê agregados. É o que permite olhar um
	// ano sem varrer milhões de linhas, sem obrigar o cliente a saber que
	// existe uma tabela de agregados.
	fonte := summary.SourceFor(rng.To.Sub(rng.From))

	// A última verificação é um fato sobre o monitor, não sobre a janela
	// escolhida: quem olha 90 dias continua querendo saber se o agendador
	// passou agora há pouco. Uma consulta serve a lista inteira.
	ultimas, err := a.store.LatestHeartbeats(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}

	items := make([]summaryResponse, 0, len(page.Items))
	for _, m := range page.Items {
		s, serie, err := a.summarise(r, m.ID, rng, fonte, buckets)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if hb, ok := ultimas[m.ID]; ok {
			quando := hb.Timestamp
			s.LastCheckAt = &quando
		}
		items = append(items, toSummaryResponse(s, serie))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"from":  rng.From,
		"to":    rng.To,
	})
}

// summarise lê a camada adequada e resume.
func (a *API) summarise(
	r *http.Request, monitorID int64, rng store.TimeRange, fonte summary.Source, buckets int,
) (summary.Summary, []summary.Point, error) {
	if fonte == summary.SourceRaw {
		// StreamHeartbeats e não QueryHeartbeats: o caminho paginado tem
		// teto, e um teto aqui truncaria a amostra em silêncio — produzindo
		// uma disponibilidade que descreve parte da janela e se apresenta
		// como se descrevesse toda ela.
		var hbs []domain.Heartbeat
		err := a.store.StreamHeartbeats(r.Context(), monitorID, rng, func(hb domain.Heartbeat) error {
			hbs = append(hbs, hb)
			return nil
		})
		if err != nil {
			return summary.Summary{}, nil, err
		}
		return summary.FromHeartbeats(monitorID, hbs), summary.Series(rng.From, rng.To, buckets, hbs), nil
	}

	res := domain.ResolutionHourly
	if fonte == summary.SourceDaily {
		res = domain.ResolutionDaily
	}
	rollups, err := a.store.QueryRollups(r.Context(), store.RollupQuery{
		MonitorID: monitorID, Resolution: res, Range: rng,
	})
	if err != nil {
		return summary.Summary{}, nil, err
	}
	return summary.FromRollups(monitorID, fonte, rollups), seriesFromRollups(rng, buckets, rollups), nil
}

// seriesFromRollups desenha a faixa a partir de agregados.
//
// Cada bucket agregado vira uma batida sintética com o pior estado do
// período e a latência mediana dele, e a divisão em buckets de tela reusa
// exatamente o mesmo caminho da série crua — assim as duas camadas não
// podem divergir na forma da figura.
func seriesFromRollups(rng store.TimeRange, buckets int, rs []domain.Rollup) []summary.Point {
	sinteticas := make([]domain.Heartbeat, 0, len(rs))
	for _, r := range rs {
		hb := domain.Heartbeat{
			MonitorID: r.MonitorID,
			Timestamp: r.BucketStart,
			Status:    rollupStatus(r),
			LatencyMS: int64(r.LatencyP50MS),
		}
		sinteticas = append(sinteticas, hb)
	}
	return summary.Series(rng.From, rng.To, buckets, sinteticas)
}

func rollupStatus(r domain.Rollup) domain.Status {
	switch {
	case r.Down > 0:
		return domain.StatusDown
	case r.Degraded > 0:
		return domain.StatusDegraded
	case r.Up > 0:
		return domain.StatusUp
	default:
		return domain.StatusUnknown
	}
}

func toSummaryResponse(s summary.Summary, serie []summary.Point) summaryResponse {
	out := summaryResponse{
		MonitorID:     s.MonitorID,
		Status:        s.Status.String(),
		Source:        string(s.Source),
		UptimePercent: s.UptimePercent,
		LatencyP50MS:  s.LatencyP50MS,
		LatencyP95MS:  s.LatencyP95MS,
		LatencyP99MS:  s.LatencyP99MS,
		LastCheckAt:   s.LastCheckAt,
		Up:            s.Up,
		Degraded:      s.Degraded,
		Down:          s.Down,
		Unknown:       s.Unknown,
		Series:        make([]point, 0, len(serie)),
	}
	for _, p := range serie {
		out.Series = append(out.Series, point{
			At: p.At, Status: p.Status.String(), LatencyMS: p.LatencyMS,
		})
	}
	return out
}
