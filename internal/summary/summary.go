// Package summary resume uma janela em estado, disponibilidade e latência.
//
// Existe no servidor porque painel, tela de detalhe e qualquer script que
// consulte a API precisam chegar ao mesmo número para a mesma janela. Esse
// cálculo já viveu na interface, duplicado entre duas telas: cada cópia
// decidia sozinha o que fazer com ausência de medição, e as duas
// discordavam sem que nada acusasse.
//
// A regra que organiza o pacote: ausência de medição é ausência, nunca
// zero. Zero é uma afirmação — "esteve fora o tempo todo", "respondeu
// instantaneamente" — e afirmá-la sobre o que não se mediu é a forma mais
// silenciosa de uma ferramenta de vigilância mentir. Por isso os campos
// numéricos são ponteiros.
package summary

import (
	"sort"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/rollup"
)

// Source é a camada de onde o resumo saiu.
type Source string

const (
	SourceRaw    Source = "raw"
	SourceHourly Source = "hourly"
	SourceDaily  Source = "daily"
)

// Limites de camada. Acima de um dia o dado cru já não cabe numa resposta
// de API sem truncar, e truncar em silêncio é o defeito que este pacote
// nasceu para não repetir.
const (
	maxRaw    = 24 * time.Hour
	maxHourly = 30 * 24 * time.Hour
)

// SourceFor escolhe a camada adequada à janela.
//
// A decisão é do servidor, e não de quem chama: assim um script recebe o
// mesmo número que a interface mostra, sem precisar saber que existe uma
// tabela de agregados.
func SourceFor(window time.Duration) Source {
	switch {
	case window <= maxRaw:
		return SourceRaw
	case window <= maxHourly:
		return SourceHourly
	default:
		return SourceDaily
	}
}

// Summary é o resumo de uma janela para um monitor.
type Summary struct {
	MonitorID int64
	Status    domain.Status
	Source    Source

	// UptimePercent é nil quando nada foi observado. Distinguir isso de
	// zero é o ponto: zero afirma indisponibilidade total.
	UptimePercent *float64

	LatencyP50MS *float64
	LatencyP95MS *float64
	LatencyP99MS *float64

	// LastCheckAt é o instante da última verificação, nil se não houve.
	LastCheckAt *time.Time

	Up       int
	Degraded int
	Down     int
	Unknown  int
}

// FromHeartbeats resume batidas cruas.
func FromHeartbeats(monitorID int64, hbs []domain.Heartbeat) Summary {
	s := Summary{MonitorID: monitorID, Status: domain.StatusUnknown, Source: SourceRaw}
	if len(hbs) == 0 {
		return s
	}

	var latencias []float64
	for _, hb := range hbs {
		switch hb.Status {
		case domain.StatusUp:
			s.Up++
		case domain.StatusDegraded:
			s.Degraded++
		case domain.StatusDown:
			s.Down++
		default:
			s.Unknown++
		}

		// Só amostra que obteve resposta entra na latência: incluir uma
		// falha como zero puxaria os percentis para baixo e faria uma queda
		// total parecer melhoria de desempenho.
		if hb.Status.Responsive() {
			latencias = append(latencias, float64(hb.LatencyMS))
		}
	}

	// A entrada chega em ordem cronológica; o estado é o da última.
	ultima := hbs[len(hbs)-1]
	s.Status = ultima.Status
	quando := ultima.Timestamp
	s.LastCheckAt = &quando

	s.UptimePercent = uptime(s.Up+s.Degraded, s.Down)
	s.setLatencias(latencias)
	return s
}

// FromRollups resume agregados horários ou diários.
//
// A camada vem por parâmetro, e não inferida das linhas: ela é a decisão
// que o servidor tomou ao ler, e uma janela sem dado nenhum precisa
// relatar de onde se tentou ler. Inferindo, uma janela vazia de noventa
// dias respondia "hourly" — descrevendo uma leitura que não aconteceu.
func FromRollups(monitorID int64, src Source, rs []domain.Rollup) Summary {
	s := Summary{MonitorID: monitorID, Status: domain.StatusUnknown, Source: src}
	if len(rs) == 0 {
		return s
	}

	for _, r := range rs {
		s.Up += r.Up
		s.Degraded += r.Degraded
		s.Down += r.Down
		s.Unknown += r.Unknown
	}

	s.Status = bucketStatus(rs[len(rs)-1])

	// LastCheckAt fica vazio de propósito: num agregado o carimbo é o
	// início do período, não o instante de uma verificação. Preenchê-lo com
	// isso faria a interface acusar de abandonado todo monitor visto numa
	// janela larga — quem sabe a resposta é a última batida, e quem a
	// conhece é o chamador.
	s.UptimePercent = uptime(s.Up+s.Degraded, s.Down)

	// O pior da janela, não a média: somar ou tirar média de percentis
	// produziria um número que não corresponde a medição alguma, enquanto o
	// pior é uma afirmação verdadeira sobre o período.
	s.LatencyP50MS = pior(rs, func(r domain.Rollup) float64 { return r.LatencyP50MS })
	s.LatencyP95MS = pior(rs, func(r domain.Rollup) float64 { return r.LatencyP95MS })
	s.LatencyP99MS = pior(rs, func(r domain.Rollup) float64 { return r.LatencyP99MS })
	return s
}

// Point é um bucket da série desenhada.
type Point struct {
	At     time.Time
	Status domain.Status
	// LatencyMS é nil quando ninguém respondeu no bucket.
	LatencyMS *float64
}

// Series divide a janela em buckets de largura igual.
//
// Entregar a série já dividida — em vez das N amostras mais recentes — é o
// que garante que a faixa e o número descrevam o mesmo período. Pedindo as
// N mais recentes, um intervalo curto fazia a figura cobrir uma hora
// enquanto o rótulo dizia vinte e quatro.
func Series(from, to time.Time, buckets int, hbs []domain.Heartbeat) []Point {
	if buckets <= 0 || !to.After(from) {
		return nil
	}

	largura := to.Sub(from) / time.Duration(buckets)
	if largura <= 0 {
		return nil
	}

	pontos := make([]Point, buckets)
	somas := make([]float64, buckets)
	respostas := make([]int, buckets)
	for i := range pontos {
		pontos[i] = Point{At: from.Add(time.Duration(i) * largura), Status: domain.StatusUnknown}
	}

	for _, hb := range hbs {
		if hb.Timestamp.Before(from) || !hb.Timestamp.Before(to) {
			continue
		}
		i := int(hb.Timestamp.Sub(from) / largura)
		if i >= buckets {
			// Só alcançável por arredondamento na fronteira final.
			i = buckets - 1
		}

		pontos[i].Status = pior2(pontos[i].Status, hb.Status)
		if hb.Status.Responsive() {
			somas[i] += float64(hb.LatencyMS)
			respostas[i]++
		}
	}

	for i := range pontos {
		if respostas[i] == 0 {
			continue
		}
		media := somas[i] / float64(respostas[i])
		pontos[i].LatencyMS = &media
	}
	return pontos
}

// pior2 escolhe o estado que mais importa entre dois.
//
// Qualquer falha no bucket pesa mais que o resto: é justamente ela que se
// procura ao olhar a faixa, e uma média a esconderia.
func pior2(a, b domain.Status) domain.Status {
	peso := func(s domain.Status) int {
		switch s {
		case domain.StatusDown:
			return 3
		case domain.StatusDegraded:
			return 2
		case domain.StatusUp:
			return 1
		default:
			return 0
		}
	}
	if peso(b) > peso(a) {
		return b
	}
	return a
}

func (s *Summary) setLatencias(latencias []float64) {
	if len(latencias) == 0 {
		return
	}
	sort.Float64s(latencias)

	// O mesmo percentil que a agregação usa. Reusar em vez de reimplementar
	// é o que impede o número da tela de divergir do número gravado no
	// agregado para a mesma amostra.
	p50 := rollup.Percentile(latencias, 50)
	p95 := rollup.Percentile(latencias, 95)
	p99 := rollup.Percentile(latencias, 99)
	s.LatencyP50MS, s.LatencyP95MS, s.LatencyP99MS = &p50, &p95, &p99
}

// uptime devolve o percentual disponível, ou nil quando nada foi
// observado sobre o alvo.
func uptime(responderam, caiu int) *float64 {
	observadas := responderam + caiu
	if observadas == 0 {
		return nil
	}
	v := float64(responderam) / float64(observadas) * 100
	return &v
}

// bucketStatus resume um período agregado num estado.
//
// Qualquer falha no intervalo pesa mais que o resto: uma hora com
// cinquenta e nove minutos no ar e um fora não é "no ar", e é justamente
// esse minuto que se procura ao olhar a faixa.
func bucketStatus(r domain.Rollup) domain.Status {
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

// pior toma o maior percentil da janela, ignorando buckets sem resposta.
func pior(rs []domain.Rollup, de func(domain.Rollup) float64) *float64 {
	var (
		max       float64
		encontrou bool
	)
	for _, r := range rs {
		v := de(r)
		if v <= 0 {
			continue
		}
		if !encontrou || v > max {
			max, encontrou = v, true
		}
	}
	if !encontrou {
		return nil
	}
	return &max
}
