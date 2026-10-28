package checker_test

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Para onde cada tipo de monitor abre conexão.
//
// É o que a política de alvos internos confere: o host do endereço HTTP,
// o host:porta do TCP e do TLS, o host do ping e o resolvedor do DNS. O
// nome consultado num monitor DNS não entra — o servidor não se conecta a
// ele, só pergunta por ele.
func TestDestinations(t *testing.T) {
	casos := []struct {
		nome string
		m    domain.Monitor
		quer []string
	}{
		{"http", domain.Monitor{Type: domain.MonitorHTTP, Target: "https://api.exemplo.com:8443/saude"}, []string{"api.exemplo.com"}},
		{"http ipv6", domain.Monitor{Type: domain.MonitorHTTP, Target: "http://[::1]:8080/"}, []string{"::1"}},
		{"tcp", domain.Monitor{Type: domain.MonitorTCP, Target: "10.0.0.5:22"}, []string{"10.0.0.5"}},
		{"tls", domain.Monitor{Type: domain.MonitorTLS, Target: "exemplo.com:443"}, []string{"exemplo.com"}},
		{"icmp", domain.Monitor{Type: domain.MonitorICMP, Target: " 192.168.0.1 "}, []string{"192.168.0.1"}},
		{"dns pelo resolvedor do sistema", domain.Monitor{Type: domain.MonitorDNS, Target: "interno.corp"}, nil},
		{"dns com resolvedor", domain.Monitor{Type: domain.MonitorDNS, Target: "exemplo.com",
			Config: json.RawMessage(`{"resolver":"10.0.0.53"}`)}, []string{"10.0.0.53"}},
		{"dns com resolvedor e porta", domain.Monitor{Type: domain.MonitorDNS, Target: "exemplo.com",
			Config: json.RawMessage(`{"resolver":"[fd00::53]:5353"}`)}, []string{"fd00::53"}},
		{"push", domain.Monitor{Type: domain.MonitorPush}, nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := checker.Destinations(c.m)
			if err != nil {
				t.Fatalf("Destinations returned unexpected error: %v", err)
			}
			if !slices.Equal(got, c.quer) {
				t.Errorf("Destinations = %v, want %v", got, c.quer)
			}
		})
	}
}

// Endereços que, alcançados a partir do servidor, são a rede de dentro
// dele: a própria máquina, a rede privada e o serviço de metadados da
// nuvem, que entrega credenciais a quem pergunta.
func TestInternalAddr(t *testing.T) {
	internos := []string{
		"127.0.0.1", "127.8.9.10", "::1", "0.0.0.0", "::",
		"10.1.2.3", "172.16.0.1", "192.168.1.1", "fd00::1",
		"169.254.169.254", "fe80::1", "100.100.100.200",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	externos := []string{"203.0.113.10", "8.8.8.8", "2001:4860:4860::8888", "172.32.0.1"}

	for _, s := range internos {
		if !checker.InternalAddr(netip.MustParseAddr(s)) {
			t.Errorf("InternalAddr(%s) = false, want true", s)
		}
	}
	for _, s := range externos {
		if checker.InternalAddr(netip.MustParseAddr(s)) {
			t.Errorf("InternalAddr(%s) = true, want false", s)
		}
	}
}
