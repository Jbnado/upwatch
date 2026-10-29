package probe_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/probe"
)

// A sonda existe para verificar, de dentro da rede privada, o que o
// central não alcança: Redis em 10.x, a API em 127.0.0.1 do contêiner
// vizinho. A conferência de alvo interno do central (issue #59) vale para
// monitores de contas restritas, e nada que a sonda lê do probe.yaml
// pode ligá-la — com ela, a sonda inteira deixaria de servir.
func TestProbeChecksReachTheInternalNetwork(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	c := probe.Check{
		Name: "porta-interna", Type: "tcp", Target: ln.Addr().String(),
		Interval: probe.Duration(time.Minute), Timeout: probe.Duration(2 * time.Second),
		// Uma chave com o nome da marca na configuração do checker não
		// chega ao monitor: a marca não é configuração de checker.
		Config: map[string]any{"restrict_internal": true},
	}
	m, err := c.Monitor(1)
	if err != nil {
		t.Fatalf("Monitor returned unexpected error: %v", err)
	}
	if m.RestrictInternal {
		t.Fatal("probe monitor is restricted to the outside network")
	}

	m.Config = nil
	if res := probe.NewRegistry().Check(context.Background(), m); res.Status != domain.StatusUp {
		t.Errorf("got %v %q, want the probe to reach 127.0.0.1", res.Status, res.Message)
	}
}
