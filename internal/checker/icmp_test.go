package checker_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// fakePinger substitui o envio real de pacotes.
//
// Separar a lógica do syscall é o que torna o ICMP testável: perda
// parcial, perda total e falta de privilégio são cenários que não se
// consegue provocar de forma confiável contra a rede de verdade.
type fakePinger struct {
	stats checker.PingStats
	err   error

	gotHost  string
	gotCount int
}

func (f *fakePinger) Ping(_ context.Context, host string, count int, _ time.Duration) (checker.PingStats, error) {
	f.gotHost, f.gotCount = host, count
	return f.stats, f.err
}

func icmpMonitor(t *testing.T, target string, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage("{}")
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}
	return domain.Monitor{
		ID: 1, Name: "ping", Type: domain.MonitorICMP, Target: target,
		Interval: time.Minute, Timeout: 3 * time.Second,
		ConfirmationThreshold: 1, Config: raw,
	}
}

func TestICMPTypeIsICMP(t *testing.T) {
	if got := checker.NewICMP().Type(); got != domain.MonitorICMP {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorICMP)
	}
}

func TestICMPAllPacketsReturnedIsUp(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{
		Sent: 3, Received: 3, AvgRTT: 12 * time.Millisecond, MinRTT: 10 * time.Millisecond, MaxRTT: 15 * time.Millisecond,
	}}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
	if got.LatencyMS != 12 {
		t.Errorf("LatencyMS = %d, want 12", got.LatencyMS)
	}
}

func TestICMPNoPacketsReturnedIsDown(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{Sent: 3, Received: 0}}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(got.Message, "3") {
		t.Errorf("Message = %q, want it to report how many packets were lost", got.Message)
	}
}

// Perda parcial não é queda, mas também não é saúde: é degradação de rede,
// e reportá-la como normal esconderia o problema até virar indisponibilidade.
func TestICMPPartialPacketLossIsDegraded(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{
		Sent: 4, Received: 3, AvgRTT: 20 * time.Millisecond,
	}}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDegraded)
	}
	if got.Meta["packet_loss"] != "25" {
		t.Errorf("Meta[packet_loss] = %q, want %q", got.Meta["packet_loss"], "25")
	}
}

// Redes onde alguma perda é normal precisam poder tolerá-la, senão o
// monitor vira fonte de ruído.
func TestICMPToleratesLossUpToConfiguredThreshold(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{
		Sent: 4, Received: 3, AvgRTT: 20 * time.Millisecond,
	}}

	got := checker.NewICMPWith(p).Check(context.Background(),
		icmpMonitor(t, "192.0.2.1", map[string]any{"max_packet_loss": 25}))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestICMPLossAboveThresholdIsDegraded(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{Sent: 4, Received: 2, AvgRTT: 20 * time.Millisecond}}

	got := checker.NewICMPWith(p).Check(context.Background(),
		icmpMonitor(t, "192.0.2.1", map[string]any{"max_packet_loss": 25}))

	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDegraded)
	}
}

// Ping cru exige CAP_NET_RAW. A mensagem precisa dizer isso, senão o
// operador procura problema na rede quando o problema é permissão do
// contêiner.
func TestICMPPermissionErrorExplainsPrivileges(t *testing.T) {
	p := &fakePinger{err: errors.New("socket: operation not permitted")}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	lower := strings.ToLower(got.Message)
	if !strings.Contains(lower, "cap_net_raw") && !strings.Contains(lower, "privilég") {
		t.Errorf("Message = %q, want it to explain the privilege requirement", got.Message)
	}
}

func TestICMPPingErrorIsDown(t *testing.T) {
	p := &fakePinger{err: errors.New("host inalcançável")}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestICMPUsesConfiguredPacketCount(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{Sent: 7, Received: 7, AvgRTT: time.Millisecond}}

	checker.NewICMPWith(p).Check(context.Background(),
		icmpMonitor(t, "192.0.2.1", map[string]any{"count": 7}))

	if p.gotCount != 7 {
		t.Errorf("sent %d packets, want 7", p.gotCount)
	}
	if p.gotHost != "192.0.2.1" {
		t.Errorf("pinged %q, want %q", p.gotHost, "192.0.2.1")
	}
}

func TestICMPDefaultsToThreePackets(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{Sent: 3, Received: 3, AvgRTT: time.Millisecond}}

	checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	if p.gotCount != checker.DefaultPingCount {
		t.Errorf("sent %d packets, want the default of %d", p.gotCount, checker.DefaultPingCount)
	}
}

func TestICMPRecordsRoundTripStatsInMeta(t *testing.T) {
	p := &fakePinger{stats: checker.PingStats{
		Sent: 3, Received: 3,
		AvgRTT: 12 * time.Millisecond, MinRTT: 8 * time.Millisecond, MaxRTT: 20 * time.Millisecond,
	}}

	got := checker.NewICMPWith(p).Check(context.Background(), icmpMonitor(t, "192.0.2.1", nil))

	for key, want := range map[string]string{
		"packets_sent":     "3",
		"packets_received": "3",
		"packet_loss":      "0",
		"rtt_min_ms":       "8",
		"rtt_max_ms":       "20",
	} {
		if got.Meta[key] != want {
			t.Errorf("Meta[%s] = %q, want %q", key, got.Meta[key], want)
		}
	}
}

func TestICMPValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := checker.NewICMP().ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

func TestICMPValidateConfigRejectsNonPositiveCount(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"count": 0})

	if err := checker.NewICMP().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a zero packet count returned nil error, want an error")
	}
}

func TestICMPValidateConfigRejectsLossOutsidePercentRange(t *testing.T) {
	for _, loss := range []float64{-1, 101} {
		cfg, _ := json.Marshal(map[string]any{"max_packet_loss": loss})
		if err := checker.NewICMP().ValidateConfig(cfg); err == nil {
			t.Errorf("ValidateConfig with max_packet_loss %v returned nil error, want an error", loss)
		}
	}
}

// Teste contra a pilha de rede real. Pula sozinho quando o ambiente não
// concede privilégio, em vez de falhar por motivo alheio ao código.
func TestICMPAgainstLoopback(t *testing.T) {
	if testing.Short() {
		t.Skip("usa a rede; pulado em -short")
	}

	got := checker.NewICMP().Check(context.Background(),
		icmpMonitor(t, "127.0.0.1", map[string]any{"count": 1}))

	if got.Status == domain.StatusDown && strings.Contains(strings.ToLower(got.Message), "privilég") {
		t.Skipf("ambiente sem privilégio para ICMP (%s): %s", runtime.GOOS, got.Message)
	}
	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v pinging loopback", got.Status, got.Message, domain.StatusUp)
	}
}
