package checker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// stub é um Checker controlável, para exercitar o registry sem depender de
// rede.
type stub struct {
	typ      domain.MonitorType
	result   checker.Result
	panicNow bool
	cfgErr   error
	observed domain.Monitor
}

func (s *stub) Type() domain.MonitorType { return s.typ }

func (s *stub) ValidateConfig(json.RawMessage) error { return s.cfgErr }

func (s *stub) Check(_ context.Context, m domain.Monitor) checker.Result {
	s.observed = m
	if s.panicNow {
		panic("checker explodiu")
	}
	return s.result
}

func newStub(typ domain.MonitorType, status domain.Status, latency int64) *stub {
	return &stub{typ: typ, result: checker.Result{Status: status, LatencyMS: latency}}
}

func monitorOf(typ domain.MonitorType) domain.Monitor {
	return domain.Monitor{
		ID: 1, Name: "alvo", Type: typ, Target: "https://example.com",
		Interval: time.Minute, Timeout: 5 * time.Second, ConfirmationThreshold: 3,
	}
}

func mustRegistry(t *testing.T, cs ...checker.Checker) *checker.Registry {
	t.Helper()
	r, err := checker.NewRegistry(cs...)
	if err != nil {
		t.Fatalf("NewRegistry returned unexpected error: %v", err)
	}
	return r
}

func TestRegistryGetReturnsRegisteredChecker(t *testing.T) {
	http := newStub(domain.MonitorHTTP, domain.StatusUp, 10)
	r := mustRegistry(t, http)

	got, err := r.Get(domain.MonitorHTTP)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got != checker.Checker(http) {
		t.Error("Get returned a different checker than the one registered")
	}
}

func TestRegistryGetRejectsUnregisteredType(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 10))

	if _, err := r.Get(domain.MonitorDNS); err == nil {
		t.Fatal("Get of an unregistered type returned nil error, want an error")
	}
}

// Dois checkers para o mesmo tipo é erro de montagem: um sobrescreveria o
// outro em silêncio e o monitor passaria a ser verificado de outro jeito.
func TestNewRegistryRejectsDuplicateType(t *testing.T) {
	_, err := checker.NewRegistry(
		newStub(domain.MonitorHTTP, domain.StatusUp, 10),
		newStub(domain.MonitorHTTP, domain.StatusDown, 0),
	)

	if err == nil {
		t.Fatal("NewRegistry with a duplicate type returned nil error, want an error")
	}
}

func TestRegistryCheckDelegatesToTheRightChecker(t *testing.T) {
	http := newStub(domain.MonitorHTTP, domain.StatusUp, 42)
	dns := newStub(domain.MonitorDNS, domain.StatusDown, 0)
	r := mustRegistry(t, http, dns)

	got := r.Check(context.Background(), monitorOf(domain.MonitorDNS))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if dns.observed.ID != 1 {
		t.Error("the DNS checker did not receive the monitor")
	}
	if http.observed.ID != 0 {
		t.Error("the HTTP checker was invoked for a DNS monitor")
	}
}

// Tipo sem checker não pode derrubar o agendador: vira resultado Down com
// a causa, igual a qualquer outra falha.
func TestRegistryCheckOnUnregisteredTypeReturnsDown(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 10))

	got := r.Check(context.Background(), monitorOf(domain.MonitorICMP))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if got.Message == "" {
		t.Error("Message is empty, want an explanation of the failure")
	}
}

// Um checker com defeito derrubaria o processo inteiro e pararia todos os
// monitores. O pânico é contido e vira Down.
func TestRegistryCheckRecoversFromPanickingChecker(t *testing.T) {
	boom := &stub{typ: domain.MonitorHTTP, panicNow: true}
	r := mustRegistry(t, boom)

	var got checker.Result
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic escaped the registry: %v", p)
			}
		}()
		got = r.Check(context.Background(), monitorOf(domain.MonitorHTTP))
	}()

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "pânico") {
		t.Errorf("Message = %q, want it to mention the panic", got.Message)
	}
}

// Detecção de resposta lenta é aplicada no registry, não em cada checker:
// centralizar garante que todo tipo de check ganhe o comportamento e que
// ele seja idêntico entre eles.
func TestRegistryCheckMarksSlowResponseAsDegraded(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 1500))
	m := monitorOf(domain.MonitorHTTP)
	m.DegradedLatency = time.Second

	got := r.Check(context.Background(), m)

	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDegraded)
	}
}

func TestRegistryCheckKeepsUpWhenWithinLatencyThreshold(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 900))
	m := monitorOf(domain.MonitorHTTP)
	m.DegradedLatency = time.Second

	got := r.Check(context.Background(), m)

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUp)
	}
}

// Limiar zero desliga a detecção; sem isso todo monitor sem configuração
// viraria degradado.
func TestRegistryCheckIgnoresLatencyWhenThresholdIsZero(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 99_999))
	m := monitorOf(domain.MonitorHTTP)
	m.DegradedLatency = 0

	got := r.Check(context.Background(), m)

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUp)
	}
}

// Um alvo fora do ar não pode ser reclassificado como apenas lento.
func TestRegistryCheckDoesNotDowngradeDownToDegraded(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusDown, 5000))
	m := monitorOf(domain.MonitorHTTP)
	m.DegradedLatency = time.Second

	got := r.Check(context.Background(), m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// networkState simula o veredito da sentinela.
type networkState struct {
	up      bool
	queried int
}

func (n *networkState) NetworkUp(context.Context) bool {
	n.queried++
	return n.up
}

// Quando a rede do próprio monitor cai, todos os alvos falham juntos. Sem
// suprimir, o operador recebe uma tempestade de alertas sobre serviços que
// continuam no ar e vai investigar o lugar errado.
func TestRegistryConvertsDownToUnknownWhenNetworkIsSuspect(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusDown, 0))
	r.WithNetworkProbe(&networkState{up: false})

	got := r.Check(context.Background(), monitorOf(domain.MonitorHTTP))

	if got.Status != domain.StatusUnknown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUnknown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "rede") {
		t.Errorf("Message = %q, want it to explain the network is suspect", got.Message)
	}
}

func TestRegistryKeepsDownWhenNetworkIsHealthy(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusDown, 0))
	r.WithNetworkProbe(&networkState{up: true})

	got := r.Check(context.Background(), monitorOf(domain.MonitorHTTP))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v: a real outage must still be reported", got.Status, domain.StatusDown)
	}
}

// Resposta bem sucedida já prova que a rede funciona; consultar a
// sentinela nesse caso seria tráfego desperdiçado no caminho feliz.
func TestRegistryDoesNotConsultNetworkProbeOnSuccess(t *testing.T) {
	probe := &networkState{up: true}
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 10))
	r.WithNetworkProbe(probe)

	got := r.Check(context.Background(), monitorOf(domain.MonitorHTTP))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUp)
	}
	if probe.queried != 0 {
		t.Errorf("network probe was consulted %d times on a successful check, want 0", probe.queried)
	}
}

func TestRegistryWithoutNetworkProbeKeepsDown(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusDown, 0))

	got := r.Check(context.Background(), monitorOf(domain.MonitorHTTP))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Status já desconhecido, como o de um monitor push sem primeiro sinal,
// não é reclassificado pela sentinela.
func TestRegistryLeavesUnknownAlone(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUnknown, 0))
	probe := &networkState{up: false}
	r.WithNetworkProbe(probe)

	got := r.Check(context.Background(), monitorOf(domain.MonitorHTTP))

	if got.Status != domain.StatusUnknown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUnknown)
	}
	if probe.queried != 0 {
		t.Errorf("network probe was consulted %d times on an already-unknown result, want 0", probe.queried)
	}
}

func TestRegistryValidateDelegatesToChecker(t *testing.T) {
	want := errors.New("config inválida")
	s := newStub(domain.MonitorHTTP, domain.StatusUp, 10)
	s.cfgErr = want
	r := mustRegistry(t, s)

	err := r.Validate(monitorOf(domain.MonitorHTTP))

	if !errors.Is(err, want) {
		t.Errorf("Validate returned %v, want %v", err, want)
	}
}

// Validate roda antes de gravar; um tipo sem checker precisa ser recusado
// aí, não virar um monitor que nunca é verificado.
func TestRegistryValidateRejectsUnregisteredType(t *testing.T) {
	r := mustRegistry(t, newStub(domain.MonitorHTTP, domain.StatusUp, 10))

	if err := r.Validate(monitorOf(domain.MonitorTLS)); err == nil {
		t.Fatal("Validate of an unregistered type returned nil error, want an error")
	}
}

func TestRegistryTypesListsRegisteredTypes(t *testing.T) {
	r := mustRegistry(t,
		newStub(domain.MonitorHTTP, domain.StatusUp, 1),
		newStub(domain.MonitorDNS, domain.StatusUp, 1),
	)

	got := r.Types()

	if len(got) != 2 {
		t.Fatalf("Types() returned %d entries, want 2", len(got))
	}
}
