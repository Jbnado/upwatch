package api

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

func originOf(o originResolver, remote string, forwarded ...string) string {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for _, f := range forwarded {
		r.Header.Add("X-Forwarded-For", f)
	}
	return o.origin(r)
}

func prefixes(t *testing.T, raw ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range raw {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Endereços do mesmo /64 dividem o balde: quem tem um /64 tem trilhões de
// endereços, e contar cada um à parte anularia o limite.
func TestIPv6AddressesInTheSame64ShareTheLimit(t *testing.T) {
	l := newRateLimiter(clock.NewFake(time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)), originResolver{}, 3, 1.0/60)

	for i, remote := range []string{
		"[2001:db8:1:2::1]:1000", "[2001:db8:1:2::2]:1001", "[2001:db8:1:2:ffff::9]:1002",
	} {
		if ok, _ := l.allow(originOf(originResolver{}, remote)); !ok {
			t.Fatalf("pedido #%d recusado antes de esgotar o balde", i+1)
		}
	}
	if ok, _ := l.allow(originOf(originResolver{}, "[2001:db8:1:2:abcd::1]:1003")); ok {
		t.Error("quarto endereço do mesmo /64 passou, want recusa: o /64 divide o balde")
	}
	if ok, _ := l.allow(originOf(originResolver{}, "[2001:db8:1:3::1]:1004")); !ok {
		t.Error("endereço de outro /64 recusado, want balde próprio")
	}
}

func TestOriginKeys(t *testing.T) {
	trusted := originResolver{trusted: prefixes(t, "10.0.0.0/8", "fd00::/8")}

	cases := []struct {
		name      string
		o         originResolver
		remote    string
		forwarded []string
		want      string
	}{
		{"ipv4 sem porta de origem", originResolver{}, "203.0.113.7:5555", nil, "203.0.113.7"},
		{"ipv4 escrito como ipv6", originResolver{}, "[::ffff:203.0.113.7]:5555", nil, "203.0.113.7"},
		{"ipv6 vira o /64", originResolver{}, "[2001:db8:1:2:3:4:5:6]:5555", nil, "2001:db8:1:2::/64"},

		// Sem proxy confiável, o cabeçalho é de quem chama e não vale.
		{"cabeçalho ignorado sem proxies", originResolver{}, "10.0.0.1:5555", []string{"198.51.100.1"}, "10.0.0.1"},
		// Com proxies, mas a conexão não veio de um deles.
		{"cabeçalho ignorado de quem não é proxy", trusted, "203.0.113.7:5555", []string{"198.51.100.1"}, "203.0.113.7"},

		{"cliente atrás de proxy confiável", trusted, "10.0.0.1:5555", []string{"198.51.100.1"}, "198.51.100.1"},
		// O da esquerda foi escrito pelo próprio cliente; vale o primeiro
		// não confiável a partir da direita.
		{"falsificação à esquerda ignorada", trusted, "10.0.0.1:5555", []string{"1.2.3.4, 198.51.100.1, 10.0.0.2"}, "198.51.100.1"},
		{"cabeçalhos repetidos", trusted, "10.0.0.1:5555", []string{"1.2.3.4", "198.51.100.1"}, "198.51.100.1"},
		{"cliente ipv6 atrás de proxy", trusted, "[fd00::1]:5555", []string{"2001:db8:9:9::1"}, "2001:db8:9:9::/64"},
		{"só proxies vale o mais à esquerda", trusted, "10.0.0.1:5555", []string{"10.0.0.3, 10.0.0.2"}, "10.0.0.3"},
		{"lixo no cabeçalho para no último proxy", trusted, "10.0.0.1:5555", []string{"198.51.100.1, lixo, 10.0.0.2"}, "10.0.0.2"},
		{"sem cabeçalho vale o proxy", trusted, "10.0.0.1:5555", nil, "10.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := originOf(tc.o, tc.remote, tc.forwarded...); got != tc.want {
				t.Errorf("origin = %q, want %q", got, tc.want)
			}
		})
	}
}
