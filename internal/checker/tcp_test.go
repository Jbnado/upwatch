package checker_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// listen abre um listener TCP local e devolve seu endereço.
func listen(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	// Aceita e descarta conexões, para o dial completar.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

// closedPort devolve um endereço que estava aberto e foi fechado.
func closedPort(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func tcpMonitor(target string, timeout time.Duration) domain.Monitor {
	return domain.Monitor{
		ID: 1, Name: "porta", Type: domain.MonitorTCP, Target: target,
		Interval: time.Minute, Timeout: timeout,
		ConfirmationThreshold: 1, Config: json.RawMessage("{}"),
	}
}

func TestTCPTypeIsTCP(t *testing.T) {
	if got := checker.NewTCP().Type(); got != domain.MonitorTCP {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorTCP)
	}
}

func TestTCPOpenPortIsUp(t *testing.T) {
	addr := listen(t)

	got := checker.NewTCP().Check(context.Background(), tcpMonitor(addr, 3*time.Second))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
	if got.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d, want a non-negative measurement", got.LatencyMS)
	}
}

func TestTCPClosedPortIsDown(t *testing.T) {
	addr := closedPort(t)

	got := checker.NewTCP().Check(context.Background(), tcpMonitor(addr, 3*time.Second))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if got.Message == "" {
		t.Error("Message is empty, want the reason the connection failed")
	}
}

func TestTCPUnresolvableHostIsDown(t *testing.T) {
	got := checker.NewTCP().Check(context.Background(), tcpMonitor("nao-existe.invalid:443", 3*time.Second))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Alvo sem porta é erro de configuração, não indisponibilidade do serviço:
// a mensagem precisa dizer isso para o operador não caçar problema de rede.
func TestTCPTargetWithoutPortIsDown(t *testing.T) {
	got := checker.NewTCP().Check(context.Background(), tcpMonitor("example.com", 3*time.Second))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "porta") {
		t.Errorf("Message = %q, want it to point at the missing port", got.Message)
	}
}

func TestTCPRecordsPortInMeta(t *testing.T) {
	addr := listen(t)
	_, port, _ := net.SplitHostPort(addr)

	got := checker.NewTCP().Check(context.Background(), tcpMonitor(addr, 3*time.Second))

	if got.Meta["port"] != port {
		t.Errorf("Meta[port] = %q, want %q", got.Meta["port"], port)
	}
}

// Endereço não roteável: o dial fica pendurado até o timeout, que é o
// comportamento que precisa ser interrompido em vez de prender o worker.
func TestTCPTimeoutIsDown(t *testing.T) {
	// 203.0.113.0/24 é reservado para documentação e não é roteável.
	m := tcpMonitor("203.0.113.1:9", 100*time.Millisecond)

	start := time.Now()
	got := checker.NewTCP().Check(context.Background(), m)
	elapsed := time.Since(start)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if elapsed > 3*time.Second {
		t.Errorf("check took %v, want it bounded by the monitor timeout", elapsed)
	}
}

func TestTCPRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := checker.NewTCP().Check(ctx, tcpMonitor("203.0.113.1:9", 5*time.Second))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestTCPValidateConfigAcceptsEmptyConfig(t *testing.T) {
	if err := checker.NewTCP().ValidateConfig(json.RawMessage("{}")); err != nil {
		t.Errorf("ValidateConfig({}) returned unexpected error: %v", err)
	}
}

func TestTCPValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := checker.NewTCP().ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}
