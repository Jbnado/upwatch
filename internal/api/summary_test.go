package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// O resumo da janela vem pronto do servidor.
//
// A interface não pode calcular disponibilidade nem percentil: quando o
// cálculo vivia lá, painel e tela de detalhe tinham cópias separadas dele
// e discordavam sobre o que fazer com ausência de medição, sem que nada
// acusasse. Estes testes fixam o contrato do lado que passou a mandar.

type resumo struct {
	MonitorID     int64      `json:"monitor_id"`
	Status        string     `json:"status"`
	Source        string     `json:"source"`
	UptimePercent *float64   `json:"uptime_percent"`
	LatencyP95MS  *float64   `json:"latency_p95_ms"`
	LastCheckAt   *time.Time `json:"last_check_at"`
	Up            int        `json:"up"`
	Down          int        `json:"down"`
	Unknown       int        `json:"unknown"`
	Series        []struct {
		At        time.Time `json:"at"`
		Status    string    `json:"status"`
		LatencyMS *float64  `json:"latency_ms"`
	} `json:"series"`
}

type resumos struct {
	Items []resumo `json:"items"`
}

// semeia cria um monitor com as batidas dadas, em minutos a partir de
// `epoch` menos a idade pedida.
func semeia(t *testing.T, s *server, nome string, beats []domain.Heartbeat) int64 {
	t.Helper()

	m := domain.Monitor{
		Name: nome, Type: domain.MonitorHTTP, Target: "https://exemplo.test",
		Interval: 60 * time.Second, Enabled: true,
		Config: []byte(`{}`),
	}
	if err := s.store.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	for i := range beats {
		beats[i].MonitorID = m.ID
	}
	if len(beats) > 0 {
		if err := s.store.WriteHeartbeats(context.Background(), beats); err != nil {
			t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
		}
	}
	return m.ID
}

func batida(atras time.Duration, status domain.Status, latencia int64) domain.Heartbeat {
	return domain.Heartbeat{
		Timestamp: epoch.Add(-atras), Status: status, LatencyMS: latencia,
	}
}

func buscaResumos(t *testing.T, s *server, query string) resumos {
	t.Helper()

	resp := s.do(t, http.MethodGet, "/api/v1/summaries"+query, nil)
	assertStatus(t, resp, http.StatusOK)
	return decode[resumos](t, resp)
}

func TestSummariesPrecisaDeAutenticacao(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// Sem login: o resumo expõe endereço e estado de alvos internos.
	resp, err := http.Get(s.URL + "/api/v1/summaries")
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSummariesTrazUmItemPorMonitor(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.login(t)

	semeia(t, s, "api", []domain.Heartbeat{batida(time.Minute, domain.StatusUp, 10)})
	semeia(t, s, "banco", []domain.Heartbeat{batida(time.Minute, domain.StatusUp, 20)})

	got := buscaResumos(t, s, "")
	if len(got.Items) != 2 {
		t.Fatalf("vieram %d resumos, want 2", len(got.Items))
	}
}

// A disponibilidade é do servidor, e exclui do denominador o que não foi
// medido.
func TestSummariesCalculaDisponibilidade(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.login(t)

	semeia(t, s, "api", []domain.Heartbeat{
		batida(4*time.Minute, domain.StatusUp, 10),
		batida(3*time.Minute, domain.StatusUp, 10),
		batida(2*time.Minute, domain.StatusDown, 0),
		batida(1*time.Minute, domain.StatusUnknown, 0),
	})

	got := buscaResumos(t, s, "")
	r := got.Items[0]

	if r.UptimePercent == nil {
		t.Fatal("uptime_percent = nil, want 66.67")
	}
	// Duas no ar, uma fora, e a sem medição fora do denominador.
	if *r.UptimePercent < 66.6 || *r.UptimePercent > 66.7 {
		t.Errorf("uptime_percent = %v, want ~66.67", *r.UptimePercent)
	}
	if r.Unknown != 1 {
		t.Errorf("unknown = %d, want 1", r.Unknown)
	}
	if r.LatencyP95MS == nil || *r.LatencyP95MS != 10 {
		t.Errorf("latency_p95_ms = %v, want 10 — o zero da queda não é medição", r.LatencyP95MS)
	}
}

// Sem medição alguma, o servidor não afirma percentual.
func TestSummariesNaoInventaNumeroSemDado(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.login(t)

	semeia(t, s, "novo", nil)

	r := buscaResumos(t, s, "").Items[0]
	if r.UptimePercent != nil {
		t.Errorf("uptime_percent = %v, want null", *r.UptimePercent)
	}
	if r.Status != "unknown" {
		t.Errorf("status = %q, want unknown", r.Status)
	}
	if r.LastCheckAt != nil {
		t.Errorf("last_check_at = %v, want null", r.LastCheckAt)
	}
}

// A série cobre a janela pedida com o número de buckets pedido.
//
// É o que impede a figura e o número de descreverem períodos diferentes —
// o defeito que fazia a faixa mostrar uma hora sob o rótulo de vinte e
// quatro.
func TestSummariesSerieCobreAJanela(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.login(t)

	// A primeira cai dentro do bucket 0, que é [-24 h, -23 h): a fronteira
	// exata pertence ao bucket seguinte, e usá-la aqui testaria o
	// arredondamento em vez da cobertura.
	semeia(t, s, "api", []domain.Heartbeat{
		batida(23*time.Hour+30*time.Minute, domain.StatusUp, 10),
		batida(time.Minute, domain.StatusUp, 10),
	})

	de := epoch.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	ate := epoch.UTC().Format(time.RFC3339)
	r := buscaResumos(t, s, fmt.Sprintf("?from=%s&to=%s&buckets=24", de, ate)).Items[0]

	if len(r.Series) != 24 {
		t.Fatalf("a série tem %d pontos, want 24", len(r.Series))
	}
	if !r.Series[0].At.Equal(epoch.Add(-24 * time.Hour)) {
		t.Errorf("o primeiro ponto é %v, want %v", r.Series[0].At, epoch.Add(-24*time.Hour))
	}

	// A batida de 23 h atrás cai no começo e a de um minuto no fim: a
	// figura precisa mostrar as duas pontas da janela.
	if r.Series[0].Status != "up" {
		t.Errorf("o primeiro ponto é %q, want up", r.Series[0].Status)
	}
	if r.Series[23].Status != "up" {
		t.Errorf("o último ponto é %q, want up", r.Series[23].Status)
	}
}

// Janela larga muda de camada sozinha, sem o cliente pedir.
func TestSummariesEscolheACamadaPelaJanela(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.login(t)
	semeia(t, s, "api", []domain.Heartbeat{batida(time.Minute, domain.StatusUp, 10)})

	casos := []struct {
		janela time.Duration
		want   string
	}{
		{6 * time.Hour, "raw"},
		{24 * time.Hour, "raw"},
		{7 * 24 * time.Hour, "hourly"},
		{90 * 24 * time.Hour, "daily"},
	}

	for _, c := range casos {
		de := epoch.Add(-c.janela).UTC().Format(time.RFC3339)
		ate := epoch.UTC().Format(time.RFC3339)
		r := buscaResumos(t, s, fmt.Sprintf("?from=%s&to=%s", de, ate)).Items[0]

		if r.Source != c.want {
			t.Errorf("janela de %v veio de %q, want %q", c.janela, r.Source, c.want)
		}
	}
}
