package summary_test

import (
	"math"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/summary"
)

// O resumo de uma janela: estado, disponibilidade e latência.
//
// Vive no servidor porque painel, tela de detalhe e qualquer script que
// consulte a API precisam chegar ao mesmo número para a mesma janela. Em
// cópias separadas — uma em Go, outra em TypeScript — cada lado decidia
// sozinho o que fazer com ausência de medição, e os dois discordavam sem
// que nada acusasse.
//
// A regra que organiza tudo aqui: ausência de medição é ausência, nunca
// zero. Zero é uma afirmação — "esteve fora o tempo todo", "respondeu
// instantaneamente" — e afirmá-la sobre o que não se mediu é a forma mais
// silenciosa de uma ferramenta de vigilância mentir.

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

func hb(offset time.Duration, status domain.Status, latency int64) domain.Heartbeat {
	return domain.Heartbeat{
		MonitorID: 7,
		Timestamp: epoch.Add(offset),
		Status:    status,
		LatencyMS: latency,
	}
}

func seg(n int) time.Duration { return time.Duration(n) * time.Second }

func TestSemDadoNaoAfirmaNada(t *testing.T) {
	s := summary.FromHeartbeats(7, nil)

	if s.Status != domain.StatusUnknown {
		t.Errorf("Status = %v, want unknown", s.Status)
	}
	if s.UptimePercent != nil {
		t.Errorf("UptimePercent = %v, want nil — sem medição não há percentual", *s.UptimePercent)
	}
	if s.LatencyP95MS != nil {
		t.Errorf("LatencyP95MS = %v, want nil", *s.LatencyP95MS)
	}
	if s.LastCheckAt != nil {
		t.Errorf("LastCheckAt = %v, want nil", *s.LastCheckAt)
	}
}

// O estado é o da amostra mais recente, não o mais frequente da janela.
func TestEstadoVemDaAmostraMaisRecente(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusUp, 10),
		hb(seg(1), domain.StatusUp, 10),
		hb(seg(2), domain.StatusDown, 0),
	})

	if s.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", s.Status)
	}
	if s.LastCheckAt == nil || !s.LastCheckAt.Equal(epoch.Add(seg(2))) {
		t.Errorf("LastCheckAt = %v, want %v", s.LastCheckAt, epoch.Add(seg(2)))
	}
}

// Unknown fica fora do denominador.
//
// Quando a rede do próprio UpWatch cai, ou quando um monitor push ainda
// não reportou, a verificação não produziu informação sobre o alvo.
// Contá-la como queda transformaria falha do observador em falha do
// observado.
func TestUnknownNaoEntraNoDenominador(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusUp, 10),
		hb(seg(1), domain.StatusUnknown, 0),
		hb(seg(2), domain.StatusUnknown, 0),
		hb(seg(3), domain.StatusUp, 10),
	})

	if s.UptimePercent == nil {
		t.Fatal("UptimePercent = nil, want 100")
	}
	if *s.UptimePercent != 100 {
		t.Errorf("UptimePercent = %v, want 100 — as duas sem medição não contam", *s.UptimePercent)
	}
	if s.Unknown != 2 {
		t.Errorf("Unknown = %d, want 2", s.Unknown)
	}
}

// Degradado respondeu: está disponível, ainda que mal.
func TestDegradadoContaComoDisponivel(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusUp, 10),
		hb(seg(1), domain.StatusDegraded, 900),
		hb(seg(2), domain.StatusDown, 0),
		hb(seg(3), domain.StatusUp, 10),
	})

	if s.UptimePercent == nil {
		t.Fatal("UptimePercent = nil")
	}
	if *s.UptimePercent != 75 {
		t.Errorf("UptimePercent = %v, want 75 — 3 de 4 responderam", *s.UptimePercent)
	}
}

// Só amostra que respondeu entra na latência.
//
// Incluir uma queda como zero puxaria os percentis para baixo e faria uma
// indisponibilidade total parecer melhoria de desempenho.
func TestQuedaNaoEntraNaLatencia(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusUp, 100),
		hb(seg(1), domain.StatusDown, 0),
		hb(seg(2), domain.StatusUp, 200),
	})

	if s.LatencyP50MS == nil {
		t.Fatal("LatencyP50MS = nil")
	}
	if *s.LatencyP50MS != 100 {
		t.Errorf("LatencyP50MS = %v, want 100 — o zero da queda não é medição", *s.LatencyP50MS)
	}
}

// Percentil por posto mais próximo, sem interpolação: todo valor
// reportado corresponde a uma latência que de fato foi medida.
func TestPercentilPorPostoMaisProximo(t *testing.T) {
	var beats []domain.Heartbeat
	for i := 1; i <= 100; i++ {
		beats = append(beats, hb(seg(i), domain.StatusUp, int64(i)))
	}
	s := summary.FromHeartbeats(7, beats)

	casos := []struct {
		nome  string
		valor *float64
		want  float64
	}{
		{"p50", s.LatencyP50MS, 50},
		{"p95", s.LatencyP95MS, 95},
		{"p99", s.LatencyP99MS, 99},
	}
	for _, c := range casos {
		if c.valor == nil {
			t.Errorf("%s = nil, want %v", c.nome, c.want)
			continue
		}
		if *c.valor != c.want {
			t.Errorf("%s = %v, want %v", c.nome, *c.valor, c.want)
		}
	}
}

// Tudo fora do ar é 0% — e isso é uma afirmação legítima, diferente de
// não ter medido.
func TestTudoForaDoArEZeroPorCento(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusDown, 0),
		hb(seg(1), domain.StatusDown, 0),
	})

	if s.UptimePercent == nil {
		t.Fatal("UptimePercent = nil, want 0 — houve medição, e ela diz que esteve fora")
	}
	if *s.UptimePercent != 0 {
		t.Errorf("UptimePercent = %v, want 0", *s.UptimePercent)
	}
	if s.LatencyP95MS != nil {
		t.Errorf("LatencyP95MS = %v, want nil — ninguém respondeu", *s.LatencyP95MS)
	}
}

// Só amostras sem medição: houve verificação, mas nenhuma informação
// sobre o alvo. Não é 0% nem 100%.
func TestSoUnknownNaoProduzPercentual(t *testing.T) {
	s := summary.FromHeartbeats(7, []domain.Heartbeat{
		hb(seg(0), domain.StatusUnknown, 0),
		hb(seg(1), domain.StatusUnknown, 0),
	})

	if s.UptimePercent != nil {
		t.Errorf("UptimePercent = %v, want nil", *s.UptimePercent)
	}
	if s.LastCheckAt == nil {
		t.Error("LastCheckAt = nil — a verificação aconteceu, ainda que sem medir o alvo")
	}
}

// ---------- agregados ----------

func rollupDe(inicio time.Duration, up, degraded, down, unknown int, p50, p95, p99 float64) domain.Rollup {
	return domain.Rollup{
		MonitorID:    7,
		Resolution:   domain.ResolutionHourly,
		BucketStart:  epoch.Add(inicio),
		Total:        up + degraded + down + unknown,
		Up:           up,
		Degraded:     degraded,
		Down:         down,
		Unknown:      unknown,
		LatencyP50MS: p50,
		LatencyP95MS: p95,
		LatencyP99MS: p99,
	}
}

func TestAgregadosSomamAsContagens(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, []domain.Rollup{
		rollupDe(0, 100, 0, 0, 0, 10, 20, 30),
		rollupDe(time.Hour, 90, 5, 5, 0, 15, 25, 35),
	})

	if s.Up != 190 || s.Degraded != 5 || s.Down != 5 {
		t.Errorf("contagens = up %d, degraded %d, down %d; want 190/5/5", s.Up, s.Degraded, s.Down)
	}
	if s.UptimePercent == nil {
		t.Fatal("UptimePercent = nil")
	}
	if math.Abs(*s.UptimePercent-97.5) > 0.001 {
		t.Errorf("UptimePercent = %v, want 97.5", *s.UptimePercent)
	}
}

// O pior da janela, não a média.
//
// Somar ou tirar média de percentis produziria um número que não
// corresponde a medição alguma. O pior é uma afirmação verdadeira sobre o
// período — e é o que se procura ao olhar uma janela larga.
func TestAgregadosUsamOPiorPercentil(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, []domain.Rollup{
		rollupDe(0, 100, 0, 0, 0, 10, 20, 30),
		rollupDe(time.Hour, 100, 0, 0, 0, 15, 900, 35),
	})

	if s.LatencyP95MS == nil {
		t.Fatal("LatencyP95MS = nil")
	}
	if *s.LatencyP95MS != 900 {
		t.Errorf("LatencyP95MS = %v, want 900", *s.LatencyP95MS)
	}
}

// Bucket sem resposta grava zero na latência. Um conjunto só de zeros
// significa que ninguém respondeu — ausência, não latência instantânea.
func TestAgregadoSoComZeroNaoAfirmaLatencia(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, []domain.Rollup{
		rollupDe(0, 0, 0, 10, 0, 0, 0, 0),
	})

	if s.LatencyP95MS != nil {
		t.Errorf("LatencyP95MS = %v, want nil", *s.LatencyP95MS)
	}
	if s.UptimePercent == nil || *s.UptimePercent != 0 {
		t.Errorf("UptimePercent = %v, want 0", s.UptimePercent)
	}
}

func TestAgregadoEstadoVemDoUltimoBucket(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, []domain.Rollup{
		rollupDe(0, 100, 0, 0, 0, 10, 20, 30),
		rollupDe(time.Hour, 0, 0, 10, 0, 0, 0, 0),
	})

	if s.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", s.Status)
	}
}

// Qualquer falha no bucket pesa mais que o resto: uma hora com cinquenta
// e nove minutos no ar e um fora não é "no ar", e é esse minuto que se
// procura ao olhar a faixa.
func TestBucketComQuedaNaoEStatusNoAr(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, []domain.Rollup{
		rollupDe(0, 59, 0, 1, 0, 10, 20, 30),
	})

	if s.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", s.Status)
	}
}

func TestAgregadosVaziosNaoAfirmamNada(t *testing.T) {
	s := summary.FromRollups(7, summary.SourceHourly, nil)

	if s.Status != domain.StatusUnknown {
		t.Errorf("Status = %v, want unknown", s.Status)
	}
	if s.UptimePercent != nil {
		t.Errorf("UptimePercent = %v, want nil", *s.UptimePercent)
	}
}

// ---------- série em buckets ----------

// A faixa e o número precisam descrever a mesma janela.
//
// Antes o painel pedia as N batidas mais recentes e rotulava "24 h": com
// intervalo curto, aquilo cobria uma hora. Entregar a série já dividida em
// buckets que cobrem a janela pedida elimina a chance de a figura e o
// número falarem de períodos diferentes.
func TestSerieCobreAJanelaComOsBucketsPedidos(t *testing.T) {
	de, ate := epoch, epoch.Add(time.Hour)

	pontos := summary.Series(de, ate, 6, []domain.Heartbeat{
		hb(0, domain.StatusUp, 10),
		hb(59*time.Minute, domain.StatusUp, 10),
	})

	if len(pontos) != 6 {
		t.Fatalf("Series devolveu %d pontos, want 6", len(pontos))
	}
	if !pontos[0].At.Equal(de) {
		t.Errorf("primeiro bucket começa em %v, want %v", pontos[0].At, de)
	}
	if !pontos[5].At.Equal(epoch.Add(50 * time.Minute)) {
		t.Errorf("último bucket começa em %v, want %v", pontos[5].At, epoch.Add(50*time.Minute))
	}
}

// Bucket sem amostra é ausência, não queda: um buraco de manutenção não
// pode ser desenhado como indisponibilidade.
func TestBucketSemAmostraEAusencia(t *testing.T) {
	pontos := summary.Series(epoch, epoch.Add(time.Hour), 4, []domain.Heartbeat{
		hb(0, domain.StatusUp, 10),
	})

	if pontos[0].Status != domain.StatusUp {
		t.Errorf("bucket 0 = %v, want up", pontos[0].Status)
	}
	for i := 1; i < 4; i++ {
		if pontos[i].Status != domain.StatusUnknown {
			t.Errorf("bucket %d = %v, want unknown", i, pontos[i].Status)
		}
		if pontos[i].LatencyMS != nil {
			t.Errorf("bucket %d tem latência %v, want nil", i, *pontos[i].LatencyMS)
		}
	}
}

// Qualquer falha no bucket pesa mais que o resto.
func TestBucketComQuedaDesenhaQueda(t *testing.T) {
	pontos := summary.Series(epoch, epoch.Add(time.Hour), 2, []domain.Heartbeat{
		hb(0, domain.StatusUp, 10),
		hb(time.Minute, domain.StatusDown, 0),
		hb(2*time.Minute, domain.StatusUp, 10),
	})

	if pontos[0].Status != domain.StatusDown {
		t.Errorf("bucket 0 = %v, want down", pontos[0].Status)
	}
}

// Fronteira pertence a um bucket só, senão a mesma amostra pesaria duas
// vezes na figura.
func TestAmostraDaFronteiraEntraNumBucketSo(t *testing.T) {
	pontos := summary.Series(epoch, epoch.Add(2*time.Hour), 2, []domain.Heartbeat{
		hb(time.Hour, domain.StatusDown, 0),
	})

	if pontos[0].Status != domain.StatusUnknown {
		t.Errorf("bucket 0 = %v, want unknown — a amostra é do segundo", pontos[0].Status)
	}
	if pontos[1].Status != domain.StatusDown {
		t.Errorf("bucket 1 = %v, want down", pontos[1].Status)
	}
}

// Amostra fora da janela é descartada em vez de deformar a borda.
func TestAmostraForaDaJanelaEIgnorada(t *testing.T) {
	pontos := summary.Series(epoch, epoch.Add(time.Hour), 2, []domain.Heartbeat{
		hb(-time.Hour, domain.StatusDown, 0),
		hb(2*time.Hour, domain.StatusDown, 0),
		hb(time.Minute, domain.StatusUp, 10),
	})

	if pontos[0].Status != domain.StatusUp {
		t.Errorf("bucket 0 = %v, want up", pontos[0].Status)
	}
	if pontos[1].Status != domain.StatusUnknown {
		t.Errorf("bucket 1 = %v, want unknown", pontos[1].Status)
	}
}

// ---------- escolha da camada ----------

// Quem decide a camada é o servidor, não quem chama.
//
// Era decisão do front, duplicada em duas telas. Com ela aqui, um script
// que consulte a API recebe o mesmo número que a interface mostra, sem
// precisar saber que existe uma tabela de agregados.
func TestCamadaEscolhidaPelaJanela(t *testing.T) {
	casos := []struct {
		janela time.Duration
		want   summary.Source
	}{
		{time.Hour, summary.SourceRaw},
		{24 * time.Hour, summary.SourceRaw},
		{25 * time.Hour, summary.SourceHourly},
		{30 * 24 * time.Hour, summary.SourceHourly},
		{31 * 24 * time.Hour, summary.SourceDaily},
		{365 * 24 * time.Hour, summary.SourceDaily},
	}

	for _, c := range casos {
		if got := summary.SourceFor(c.janela); got != c.want {
			t.Errorf("SourceFor(%v) = %v, want %v", c.janela, got, c.want)
		}
	}
}
