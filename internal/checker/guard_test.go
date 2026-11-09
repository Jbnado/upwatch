package checker_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Conferência do alvo interno no momento da conexão (issue #59).
//
// O cadastro recusa, para conta restrita, o alvo que resolve para a rede
// interna do servidor — mas resolve o nome naquela hora. Depois disso o
// nome pode passar a apontar para 127.0.0.1 ou para o metadado da nuvem
// (DNS rebinding), e um endereço de fora pode responder 302 para dentro.
// Nos dois casos quem conecta é o checker, e é ele que precisa olhar o
// endereço efetivamente conectado.

// internalMessage é o trecho que a causa da recusa sempre traz.
const internalMessage = "rede interna do servidor"

// loopback2 faz o papel da rede interna nos testes: 127.0.0.1 passa a
// ser "de fora", e 127.0.0.2 é o endereço que a regra precisa barrar.
// Assim rebinding e redirecionamento rodam sem rede, só na máquina.
var loopback2 = netip.MustParseAddr("127.0.0.2")

func onlyLoopback2IsInternal(a netip.Addr) bool { return a.Unmap() == loopback2 }

func restricted(m domain.Monitor) domain.Monitor {
	m.RestrictInternal = true
	return m
}

// listenOn abre um listener TCP que aceita e fecha conexões.
func listenOn(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Listen(%s) returned unexpected error: %v", addr, err)
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
	return ln
}

func assertRefusedAsInternal(t *testing.T, res checker.Result) {
	t.Helper()
	if res.Status != domain.StatusDown || !strings.Contains(res.Message, internalMessage) {
		t.Fatalf("got %v %q, want down citing the internal network", res.Status, res.Message)
	}
}

// Um monitor salvo por conta restrita e apontado para dentro sem passar
// pelo cadastro (o caso de qualquer nome que mude depois) cai na conexão.
// O mesmo alvo, sem a restrição, continua verificado: é o monitor do
// administrador de escopo total, e é a sonda dentro da rede privada.
func TestRestrictedMonitorIsRefusedOnConnectToInternalAddress(t *testing.T) {
	ln := listenOn(t, "127.0.0.1:0")
	m := tcpMonitor(ln.Addr().String(), 2*time.Second)

	assertRefusedAsInternal(t, checker.NewTCP().Check(context.Background(), restricted(m)))

	if res := checker.NewTCP().Check(context.Background(), m); res.Status != domain.StatusUp {
		t.Errorf("unrestricted: got %v %q, want up", res.Status, res.Message)
	}
}

// O endereço não pode aparecer na causa. Ela é lida por quem cadastrou o
// monitor, e dizer para onde o nome resolveu do lado de dentro entregaria
// o DNS interno do servidor a quem só tem um grupo.
func TestInternalRefusalDoesNotRevealTheAddress(t *testing.T) {
	ln := listenOn(t, "127.0.0.1:0")

	res := checker.NewTCP().Check(context.Background(), restricted(tcpMonitor(ln.Addr().String(), 2*time.Second)))

	assertRefusedAsInternal(t, res)
	if strings.Contains(res.Message, "127.0.0.1") {
		t.Errorf("Message = %q reveals the resolved address", res.Message)
	}
}

// rebindingResolver responde rebind.test. com 127.0.0.1 ("de fora") até
// flip ser chamado, e com 127.0.0.2 ("de dentro") depois.
func rebindingResolver(t *testing.T) (*net.Resolver, func()) {
	t.Helper()
	var inside atomic.Bool
	srv := newDNSServer(t, func(req *dns.Msg) *dns.Msg {
		resp := &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}}
		if len(req.Question) == 1 && req.Question[0].Qtype == dns.TypeA {
			ip := "127.0.0.1"
			if inside.Load() {
				ip = "127.0.0.2"
			}
			resp.Answer = []dns.RR{mustRR(t, "rebind.test. 0 IN A "+ip)}
		}
		return resp
	})
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", srv.addr)
		},
	}
	return r, func() { inside.Store(true) }
}

// O nome resolvia para fora quando o monitor foi salvo e passa a
// resolver para dentro: a verificação seguinte precisa cair, sem que
// ninguém tenha editado nada.
func TestRestrictedMonitorFollowsDNSRebinding(t *testing.T) {
	outside := listenOn(t, "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(outside.Addr().String())
	listenOn(t, "127.0.0.2:"+port)

	resolver, flip := rebindingResolver(t)
	checker.SetNetworkForTest(t, onlyLoopback2IsInternal, resolver)

	m := tcpMonitor("rebind.test:"+port, 2*time.Second)
	if res := checker.NewTCP().Check(context.Background(), restricted(m)); res.Status != domain.StatusUp {
		t.Fatalf("before rebinding: got %v %q, want up", res.Status, res.Message)
	}

	flip()

	assertRefusedAsInternal(t, checker.NewTCP().Check(context.Background(), restricted(m)))
	if res := checker.NewTCP().Check(context.Background(), m); res.Status != domain.StatusUp {
		t.Errorf("unrestricted after rebinding: got %v %q, want up", res.Status, res.Message)
	}
}

// Um endereço de fora responde 302 para dentro. Seguir o redirecionamento
// é o padrão, e cada salto é uma conexão nova que precisa da mesma regra.
func TestRestrictedHTTPRefusesRedirectIntoInternalNetwork(t *testing.T) {
	checker.SetNetworkForTest(t, onlyLoopback2IsInternal, nil)

	inside := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("credencial do metadado"))
	}))
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatalf("Listen returned unexpected error: %v", err)
	}
	inside.Listener = ln
	inside.Start()
	t.Cleanup(inside.Close)

	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inside.URL+"/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(outside.Close)

	m := httpMonitor(t, outside.URL, map[string]any{"body_contains": "credencial"})
	res := checker.NewHTTP().Check(context.Background(), restricted(m))

	assertRefusedAsInternal(t, res)
	if strings.Contains(res.Message, "127.0.0.2") {
		t.Errorf("Message = %q reveals the redirect target address", res.Message)
	}

	if res := checker.NewHTTP().Check(context.Background(), m); res.Status != domain.StatusUp {
		t.Errorf("unrestricted: got %v %q, want up following the redirect", res.Status, res.Message)
	}
}

// Sem redirecionamento, o endereço de fora continua verificado: a regra
// barra a rede interna, não o monitor restrito.
func TestRestrictedHTTPReachesOutsideAddress(t *testing.T) {
	checker.SetNetworkForTest(t, onlyLoopback2IsInternal, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)

	if res := checker.NewHTTP().Check(context.Background(), restricted(httpMonitor(t, srv.URL, nil))); res.Status != domain.StatusUp {
		t.Errorf("got %v %q, want up", res.Status, res.Message)
	}
}

// Todo checker que abre conexão a partir do servidor passa pela regra.
// Um tipo esquecido aqui seria a porta que a correção deixou aberta.
func TestEveryCheckerRefusesInternalAddressWhenRestricted(t *testing.T) {
	ln := listenOn(t, "127.0.0.1:0")
	addr := ln.Addr().String()
	pinger := &fakePinger{stats: checker.PingStats{Sent: 1, Received: 1}}

	for _, tc := range []struct {
		name string
		c    checker.Checker
		m    domain.Monitor
	}{
		{"http", checker.NewHTTP(), httpMonitor(t, "http://"+addr+"/", nil)},
		{"tcp", checker.NewTCP(), tcpMonitor(addr, 2*time.Second)},
		{"tls", checker.NewTLS(), tlsMonitor(t, addr, nil)},
		{"redis", checker.NewRedis(), redisMonitor(addr, "{}")},
		{"redis com tls", checker.NewRedis(), redisMonitor(addr, `{"tls":true}`)},
		{"postgres", checker.NewPostgres(), sqlMonitor(domain.MonitorPostgres, addr, `{"username":"monitor"}`)},
		{"mysql", checker.NewMySQL(), sqlMonitor(domain.MonitorMySQL, addr, `{"username":"monitor"}`)},
		{"dns", checker.NewDNS(), dnsMonitor(t, "exemplo.com", map[string]any{"resolver": addr})},
		{"icmp", checker.NewICMPWith(pinger), icmpMonitor(t, "127.0.0.1", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusedAsInternal(t, tc.c.Check(context.Background(), restricted(tc.m)))
		})
	}

	// O ping não pode nem sair: o pacote já seria a sondagem.
	if pinger.gotHost != "" {
		t.Errorf("pinger was called with %q despite the refusal", pinger.gotHost)
	}
}

// O ping vai ao endereço conferido, e não ao nome: resolver de novo na
// hora de enviar abriria a mesma janela de rebinding que a regra fecha.
func TestRestrictedICMPPingsTheCheckedAddress(t *testing.T) {
	resolver, _ := rebindingResolver(t)
	checker.SetNetworkForTest(t, onlyLoopback2IsInternal, resolver)
	pinger := &fakePinger{stats: checker.PingStats{Sent: 1, Received: 1}}

	res := checker.NewICMPWith(pinger).Check(context.Background(), restricted(icmpMonitor(t, "rebind.test", nil)))

	if res.Status != domain.StatusUp || pinger.gotHost != "127.0.0.1" {
		t.Errorf("got %v %q pinging %q, want up pinging 127.0.0.1", res.Status, res.Message, pinger.gotHost)
	}
}

// O DNS sem resolvedor configurado usa o do sistema, que o operador
// escolheu ao instalar e que costuma morar na rede interna (127.0.0.53 no
// systemd-resolved). O cadastro não o confere, e a conexão também não.
func TestRestrictedDNSWithSystemResolverIsNotRefused(t *testing.T) {
	res := checker.NewDNS().Check(context.Background(), restricted(dnsMonitor(t, "localhost", nil)))

	if strings.Contains(res.Message, internalMessage) {
		t.Errorf("Message = %q, want the system resolver not to be refused", res.Message)
	}
}

// Com UPWATCH_RESTRICT_PRIVATE_TARGETS desligada, quem opera o servidor
// escolheu deixar as contas restritas alcançarem a rede interna. O
// monitor salvo antes, com a regra ligada, não pode continuar barrado só
// porque ninguém o salvou de novo.
func TestRegistryCanIgnoreTheRestriction(t *testing.T) {
	ln := listenOn(t, "127.0.0.1:0")
	reg, err := checker.NewRegistry(checker.NewTCP())
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	m := restricted(tcpMonitor(ln.Addr().String(), 2*time.Second))

	assertRefusedAsInternal(t, reg.Check(context.Background(), m))

	reg.IgnoreInternalRestriction()
	if res := reg.Check(context.Background(), m); res.Status != domain.StatusUp {
		t.Errorf("got %v %q, want up with the restriction off", res.Status, res.Message)
	}
}

// O Control do dialer restrito usa a mesma InternalAddr do cadastro:
// endereço IPv6 que carrega um IPv4 (NAT64, 6to4, IPv4-compatível,
// SIIT) e as faixas reservadas são recusados também na conexão. Chamado
// direto porque discar para eles dependeria da rede de quem roda o teste.
func TestRestrictedDialRefusesEmbeddedIPv4AndReservedRanges(t *testing.T) {
	for _, addr := range []string{
		"[64:ff9b::a00:5]:80", "[64:ff9b:1::a9fe:a9fe]:80", "[2002:a00:5::1]:80",
		"[::a00:5]:80", "[::ffff:0:a00:5]:80", "[fec0::1]:80",
		"198.18.0.1:80", "192.0.0.170:80", "240.0.0.1:80", "255.255.255.255:80",
	} {
		if err := checker.RefuseInternalForTest(context.Background(), "tcp", addr, nil); !errors.Is(err, checker.ErrInternalTarget) {
			t.Errorf("refuseInternal(%s) = %v, want ErrInternalTarget", addr, err)
		}
	}
	if err := checker.RefuseInternalForTest(context.Background(), "tcp", "203.0.113.10:80", nil); err != nil {
		t.Errorf("refuseInternal(203.0.113.10:80) = %v, want nil", err)
	}
}

// O monitor DNS restrito sem resolvedor próprio pergunta ao do sistema,
// que conhece a zona interna. O endereço interno da resposta não pode
// aparecer na causa nem nos detalhes, e não conta como resposta; o
// endereço de fora continua aparecendo, e o monitor continua útil.
func TestRestrictedDNSHidesInternalAnswers(t *testing.T) {
	srv := newDNSServer(t, answering(
		mustRR(t, "misto.test. 60 IN A 10.0.0.5"),
		mustRR(t, "misto.test. 60 IN A 203.0.113.7"),
	))
	checker.SetSystemResolverForTest(t, srv.addr)

	res := checker.NewDNS().Check(context.Background(), restricted(dnsMonitor(t, "misto.test", nil)))
	if res.Status != domain.StatusUp || res.Meta["answers"] != "203.0.113.7" || res.Meta["answer_count"] != "1" {
		t.Errorf("restricted: got %v %q %v, want up showing only the outside address", res.Status, res.Message, res.Meta)
	}

	// Sem a restrição, nada muda.
	res = checker.NewDNS().Check(context.Background(), dnsMonitor(t, "misto.test", nil))
	if !strings.Contains(res.Meta["answers"], "10.0.0.5") {
		t.Errorf("unrestricted: Meta[answers] = %q, want the internal address", res.Meta["answers"])
	}
}

// Nome que só resolve para dentro: cai como nome sem registro, e o valor
// esperado igual ao endereço interno não o confirma.
func TestRestrictedDNSDoesNotConfirmInternalAddresses(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "db.corp.internal. 60 IN A 10.0.0.5")))
	checker.SetSystemResolverForTest(t, srv.addr)

	res := checker.NewDNS().Check(context.Background(), restricted(
		dnsMonitor(t, "db.corp.internal", map[string]any{"expected_values": []string{"10.0.0.5"}})))
	if res.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", res.Status)
	}
	if strings.Contains(res.Meta["answers"], "10.0.0.5") || res.Meta["answer_count"] != "0" {
		t.Errorf("Meta = %v reveals the internal answer", res.Meta)
	}
}

// Registro que não é endereço não tem como ser julgado: pelo resolvedor
// do sistema, o valor não aparece, mas a comparação com o esperado
// continua valendo.
func TestRestrictedDNSHidesNonAddressAnswersFromTheSystemResolver(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, `cfg.corp.internal. 60 IN TXT "senha-interna"`)))
	checker.SetSystemResolverForTest(t, srv.addr)

	res := checker.NewDNS().Check(context.Background(), restricted(
		dnsMonitor(t, "cfg.corp.internal", map[string]any{"record_type": "TXT", "expected_values": []string{"outra"}})))
	if res.Status != domain.StatusDown {
		t.Errorf("Status = %v, want down", res.Status)
	}
	for k, v := range res.Meta {
		if strings.Contains(v, "senha-interna") {
			t.Errorf("Meta[%s] = %q reveals the answer", k, v)
		}
	}
	if strings.Contains(res.Message, "senha-interna") {
		t.Errorf("Message = %q reveals the answer", res.Message)
	}

	res = checker.NewDNS().Check(context.Background(), restricted(
		dnsMonitor(t, "cfg.corp.internal", map[string]any{"record_type": "TXT", "expected_values": []string{"senha-interna"}})))
	if res.Status != domain.StatusUp {
		t.Errorf("matching expected value: got %v %q, want up", res.Status, res.Message)
	}
}
