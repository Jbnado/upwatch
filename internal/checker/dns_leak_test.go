package checker_test

import (
	"context"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// guard.go declara que a causa não traz endereço, porque dizer para onde o
// nome resolveu do lado de dentro entregaria o DNS interno do servidor a
// uma conta que só tem um grupo. Isso valia no caminho da recusa e não no
// da falha: o resolvedor do sistema vem do resolv.conf do contêiner, e a
// causa saía com o endereço dele — legível em
// GET /monitors/{id}/heartbeats por quem é dono do grupo.
//
// Bastava um timeout curto para produzir essa causa a cada batida.
func TestRestrictedDNSFailureDoesNotNameTheSystemResolver(t *testing.T) {
	// Um resolvedor que não responde, no endereço de uma rede interna.
	const interno = "10.20.0.2:53"
	checker.SetSystemResolverForTest(t, interno)

	m := restricted(dnsMonitor(t, "db.corp.internal", nil))
	res := checker.NewDNS().Check(context.Background(), m)

	if res.Status != domain.StatusDown {
		t.Fatalf("status = %v, want down", res.Status)
	}
	for _, pedaço := range []string{interno, "10.20.0.2", "10.20.0."} {
		if strings.Contains(res.Message, pedaço) {
			t.Errorf("a causa entregou o resolvedor de dentro (%q): %s", pedaço, res.Message)
		}
	}
	// E continua dizendo o que houve, senão o diagnóstico fica impossível.
	if !strings.Contains(res.Message, "resolvedor do sistema") {
		t.Errorf("a causa não diz o que falhou: %s", res.Message)
	}
}

// Com resolvedor escolhido no cadastro o endereço é conhecido de quem
// cadastrou, e aí ele fica: esconder só atrapalharia o diagnóstico.
func TestChosenResolverStillAppearsInTheCause(t *testing.T) {
	const escolhido = "203.0.113.9:53"

	m := restricted(dnsMonitor(t, "exemplo.com", map[string]any{"resolver": escolhido}))
	res := checker.NewDNS().Check(context.Background(), m)

	if res.Status != domain.StatusDown {
		t.Fatalf("status = %v, want down", res.Status)
	}
	if !strings.Contains(res.Message, "203.0.113.9") {
		t.Errorf("a causa esconde o resolvedor que o cadastro escolheu: %s", res.Message)
	}
}

// restrictedAnswers esconde o valor interno, mas o desfecho distinguia
// três casos: nome que não existe (NXDOMAIN), nome que existe só com
// endereço interno ("nenhum registro A para ..."), e nome público (up).
//
// Com isso, quem pode trocar o alvo do monitor enumera a zona interna um
// nome por vez. As duas recusas passam a dar a mesma causa.
func TestRestrictedDNSDoesNotTellInternalOnlyApartFromMissing(t *testing.T) {
	sóInterno := newDNSServer(t, answering(mustRR(t, "db.corp.internal. 60 IN A 10.0.0.5")))
	checker.SetSystemResolverForTest(t, sóInterno.addr)
	interno := checker.NewDNS().Check(context.Background(),
		restricted(dnsMonitor(t, "db.corp.internal", nil)))

	semNome := newDNSServer(t, func(q *dns.Msg) *dns.Msg {
		r := new(dns.Msg)
		r.SetRcode(q, dns.RcodeNameError)
		return r
	})
	checker.SetSystemResolverForTest(t, semNome.addr)
	ausente := checker.NewDNS().Check(context.Background(),
		restricted(dnsMonitor(t, "nao-existe.corp.internal", nil)))

	if interno.Status != domain.StatusDown || ausente.Status != domain.StatusDown {
		t.Fatalf("status = %v e %v, want os dois down", interno.Status, ausente.Status)
	}
	if strings.Contains(interno.Message, "nenhum registro") {
		t.Errorf("a causa conta que o nome existe e resolve para dentro: %s", interno.Message)
	}
	if strings.Contains(interno.Message, "db.corp.internal") {
		t.Errorf("a causa repete o nome consultado: %s", interno.Message)
	}
	if interno.Message == ausente.Message {
		return
	}
	// Se não forem idênticas, ao menos a do caso interno não pode revelar
	// que o nome existe — o que as asserções acima já exigem. Mas o alvo é
	// serem iguais, porque é a igualdade que fecha o oráculo.
	t.Errorf("causas distinguíveis: interno %q vs ausente %q", interno.Message, ausente.Message)
}
