package checker_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// dnsServer é um servidor DNS local. Responder de dentro do teste elimina
// a dependência de rede externa e permite exercitar NXDOMAIN, SERVFAIL e
// resposta vazia, que não se consegue provocar num resolvedor real.
type dnsServer struct {
	addr string

	mu      sync.Mutex
	queries []dns.Question
}

func newDNSServer(t *testing.T, handler func(*dns.Msg) *dns.Msg) *dnsServer {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket returned unexpected error: %v", err)
	}

	s := &dnsServer{addr: pc.LocalAddr().String()}

	srv := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			s.mu.Lock()
			s.queries = append(s.queries, req.Question...)
			s.mu.Unlock()

			resp := handler(req)
			if resp == nil {
				return // silêncio: força o timeout do cliente
			}
			// SetReply zera Rcode e Answer, então eles são reaplicados
			// depois de o cabeçalho ser preparado.
			rcode, answer := resp.Rcode, resp.Answer
			resp.SetReply(req)
			resp.Rcode, resp.Answer = rcode, answer
			_ = w.WriteMsg(resp)
		}),
	}

	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("DNS test server did not start")
	}
	return s
}

func (s *dnsServer) asked() []dns.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dns.Question(nil), s.queries...)
}

// answering devolve um handler que responde com os registros dados.
func answering(records ...dns.RR) func(*dns.Msg) *dns.Msg {
	return func(*dns.Msg) *dns.Msg {
		return &dns.Msg{Answer: records, MsgHdr: dns.MsgHdr{Rcode: dns.RcodeSuccess}}
	}
}

func rcode(code int) func(*dns.Msg) *dns.Msg {
	return func(*dns.Msg) *dns.Msg {
		return &dns.Msg{MsgHdr: dns.MsgHdr{Rcode: code}}
	}
}

func mustRR(t *testing.T, s string) dns.RR {
	t.Helper()
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("NewRR(%q) returned unexpected error: %v", s, err)
	}
	return rr
}

func dnsMonitor(t *testing.T, target string, cfg map[string]any) domain.Monitor {
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
		ID: 1, Name: "dns", Type: domain.MonitorDNS, Target: target,
		Interval: time.Minute, Timeout: 3 * time.Second,
		ConfirmationThreshold: 1, Config: raw,
	}
}

func checkDNS(t *testing.T, target string, cfg map[string]any) checker.Result {
	t.Helper()
	return checker.NewDNS().Check(context.Background(), dnsMonitor(t, target, cfg))
}

func TestDNSTypeIsDNS(t *testing.T) {
	if got := checker.NewDNS().Type(); got != domain.MonitorDNS {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorDNS)
	}
}

func TestDNSResolvingNameIsUp(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "exemplo.com. 300 IN A 203.0.113.10")))

	got := checkDNS(t, "exemplo.com", map[string]any{"resolver": srv.addr})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
	if got.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d, want a non-negative measurement", got.LatencyMS)
	}
}

func TestDNSNonexistentDomainIsDown(t *testing.T) {
	srv := newDNSServer(t, rcode(dns.RcodeNameError))

	got := checkDNS(t, "nao-existe.exemplo", map[string]any{"resolver": srv.addr})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "nxdomain") {
		t.Errorf("Message = %q, want it to name NXDOMAIN", got.Message)
	}
}

func TestDNSServerFailureIsDown(t *testing.T) {
	srv := newDNSServer(t, rcode(dns.RcodeServerFailure))

	got := checkDNS(t, "exemplo.com", map[string]any{"resolver": srv.addr})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Resposta bem sucedida porém sem registro significa que o nome existe
// mas não tem o tipo pedido — o serviço não é alcançável mesmo assim.
func TestDNSEmptyAnswerIsDown(t *testing.T) {
	srv := newDNSServer(t, answering())

	got := checkDNS(t, "exemplo.com", map[string]any{"resolver": srv.addr})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "nenhum registro") {
		t.Errorf("Message = %q, want it to say no record was returned", got.Message)
	}
}

func TestDNSRecordTypes(t *testing.T) {
	tests := []struct {
		recordType string
		record     string
		want       string
	}{
		{"A", "exemplo.com. 300 IN A 203.0.113.10", "203.0.113.10"},
		{"AAAA", "exemplo.com. 300 IN AAAA 2001:db8::1", "2001:db8::1"},
		{"CNAME", "exemplo.com. 300 IN CNAME destino.exemplo.com.", "destino.exemplo.com."},
		{"MX", "exemplo.com. 300 IN MX 10 mail.exemplo.com.", "mail.exemplo.com."},
		{"TXT", `exemplo.com. 300 IN TXT "v=spf1 -all"`, "v=spf1 -all"},
		{"NS", "exemplo.com. 300 IN NS ns1.exemplo.com.", "ns1.exemplo.com."},
	}

	for _, tt := range tests {
		t.Run(tt.recordType, func(t *testing.T) {
			srv := newDNSServer(t, answering(mustRR(t, tt.record)))

			got := checkDNS(t, "exemplo.com", map[string]any{
				"resolver":    srv.addr,
				"record_type": tt.recordType,
			})

			if got.Status != domain.StatusUp {
				t.Fatalf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
			}
			if !strings.Contains(got.Meta["answers"], tt.want) {
				t.Errorf("Meta[answers] = %q, want it to contain %q", got.Meta["answers"], tt.want)
			}
		})
	}
}

// Registro apontando para o lugar errado é falha silenciosa: o nome
// resolve, então um monitor que só verifica "resolveu?" não percebe um
// sequestro de DNS nem uma migração mal feita.
func TestDNSExpectedValueMatching(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "exemplo.com. 300 IN A 203.0.113.10")))

	tests := []struct {
		name     string
		expected []string
		want     domain.Status
	}{
		{"valor esperado presente", []string{"203.0.113.10"}, domain.StatusUp},
		{"um entre vários", []string{"198.51.100.1", "203.0.113.10"}, domain.StatusUp},
		{"valor inesperado", []string{"198.51.100.1"}, domain.StatusDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkDNS(t, "exemplo.com", map[string]any{
				"resolver":        srv.addr,
				"expected_values": tt.expected,
			})
			if got.Status != tt.want {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, tt.want)
			}
		})
	}
}

// Verificar de qual resolvedor a resposta veio é o próprio ponto do
// recurso: comparar o DNS interno com o público expõe divergência de zona.
func TestDNSUsesConfiguredResolver(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "exemplo.com. 300 IN A 203.0.113.10")))

	checkDNS(t, "exemplo.com", map[string]any{"resolver": srv.addr})

	asked := srv.asked()
	if len(asked) == 0 {
		t.Fatal("the configured resolver received no query")
	}
	if asked[0].Name != "exemplo.com." {
		t.Errorf("resolver was asked for %q, want %q", asked[0].Name, "exemplo.com.")
	}
}

func TestDNSResolverWithoutPortIsAccepted(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "exemplo.com. 300 IN A 203.0.113.10")))
	host, _, _ := net.SplitHostPort(srv.addr)

	// Sem porta o padrão 53 seria usado, e o servidor de teste está noutra;
	// o objetivo aqui é só confirmar que a config é aceita e o erro vira
	// resultado, não pânico.
	got := checkDNS(t, "exemplo.com", map[string]any{"resolver": host})

	if got.Status != domain.StatusDown && got.Status != domain.StatusUp {
		t.Errorf("Status = %v, want a definite result rather than a crash", got.Status)
	}
}

func TestDNSUnreachableResolverIsDown(t *testing.T) {
	m := dnsMonitor(t, "exemplo.com", map[string]any{"resolver": "203.0.113.1:53"})
	m.Timeout = 200 * time.Millisecond

	got := checker.NewDNS().Check(context.Background(), m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestDNSRespectsCancelledContext(t *testing.T) {
	srv := newDNSServer(t, answering(mustRR(t, "exemplo.com. 300 IN A 203.0.113.10")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := checker.NewDNS().Check(ctx, dnsMonitor(t, "exemplo.com", map[string]any{"resolver": srv.addr}))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestDNSRecordsAnswerCountInMeta(t *testing.T) {
	srv := newDNSServer(t,
		answering(
			mustRR(t, "exemplo.com. 300 IN A 203.0.113.10"),
			mustRR(t, "exemplo.com. 300 IN A 203.0.113.11"),
		))

	got := checkDNS(t, "exemplo.com", map[string]any{"resolver": srv.addr})

	if got.Meta["answer_count"] != "2" {
		t.Errorf("Meta[answer_count] = %q, want %q", got.Meta["answer_count"], "2")
	}
	if got.Meta["record_type"] != "A" {
		t.Errorf("Meta[record_type] = %q, want %q", got.Meta["record_type"], "A")
	}
}

func TestDNSValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := checker.NewDNS().ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

func TestDNSValidateConfigRejectsUnknownRecordType(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"record_type": "BANANA"})

	if err := checker.NewDNS().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with an unknown record type returned nil error, want an error")
	}
}

func TestDNSValidateConfigAcceptsKnownRecordTypes(t *testing.T) {
	for _, rt := range []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS", "a", "txt"} {
		t.Run(rt, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"record_type": rt})
			if err := checker.NewDNS().ValidateConfig(cfg); err != nil {
				t.Errorf("ValidateConfig(%q) returned unexpected error: %v", rt, err)
			}
		})
	}
}
