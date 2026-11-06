package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/Jbnado/upwatch/internal/domain"
)

// defaultDNSPort é a porta assumida quando o resolvedor é informado só
// pelo endereço.
const defaultDNSPort = "53"

// recordTypes são os tipos de registro atendidos.
var recordTypes = map[string]uint16{
	"A":     dns.TypeA,
	"AAAA":  dns.TypeAAAA,
	"CNAME": dns.TypeCNAME,
	"MX":    dns.TypeMX,
	"TXT":   dns.TypeTXT,
	"NS":    dns.TypeNS,
}

// DNSConfig é a configuração de um monitor DNS.
type DNSConfig struct {
	// RecordType é o tipo consultado. Vazio assume A.
	RecordType string `json:"record_type,omitempty"`

	// ExpectedValues exige que a resposta contenha ao menos um destes.
	//
	// Sem isso o monitor só saberia dizer que o nome resolve, e um
	// sequestro de DNS ou uma migração mal feita passariam despercebidos:
	// o nome continua resolvendo, só que para o lugar errado.
	ExpectedValues []string `json:"expected_values,omitempty"`

	// Resolver é o servidor consultado, em host ou host:porta. Vazio usa o
	// resolvedor do sistema.
	//
	// Apontar para um servidor específico é o que permite comparar a zona
	// interna com a pública e detectar divergência entre elas.
	Resolver string `json:"resolver,omitempty"`
}

func (c DNSConfig) recordType() (string, uint16) {
	name := strings.ToUpper(c.RecordType)
	if name == "" {
		name = "A"
	}
	return name, recordTypes[name]
}

// DNSChecker resolve um nome e valida a resposta.
type DNSChecker struct {
	client *dns.Client
}

// NewDNS cria o checker DNS.
func NewDNS() *DNSChecker {
	return &DNSChecker{client: &dns.Client{}}
}

// Type identifica o tipo de monitor atendido.
func (c *DNSChecker) Type() domain.MonitorType { return domain.MonitorDNS }

// ValidateConfig confere a configuração no cadastro.
func (c *DNSChecker) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parseDNSConfig(raw)
	if err != nil {
		return err
	}
	if cfg.RecordType != "" {
		if _, ok := recordTypes[strings.ToUpper(cfg.RecordType)]; !ok {
			return fmt.Errorf("checker: tipo de registro DNS desconhecido %q", cfg.RecordType)
		}
	}
	return nil
}

// Check consulta o nome e avalia a resposta.
func (c *DNSChecker) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseDNSConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	typeName, qtype := cfg.recordType()
	if qtype == 0 {
		return down("tipo de registro DNS desconhecido %q", cfg.RecordType)
	}

	server, err := c.resolverAddr(cfg)
	if err != nil {
		return down("%v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(strings.TrimSpace(m.Target)), qtype)

	client := c.client
	if m.RestrictInternal && cfg.Resolver != "" {
		// Só o resolvedor escolhido no cadastro é conferido. O do sistema
		// foi escolhido por quem instalou e costuma morar na rede interna
		// (127.0.0.53 no systemd-resolved); o cadastro também não o
		// confere (ver Destinations).
		restricted := *c.client
		restricted.Dialer = dialerFor(m)
		client = &restricted
	}

	start := time.Now()
	resp, _, err := client.ExchangeContext(ctx, msg, server)
	latency := time.Since(start).Milliseconds()
	if refused, ok := internalRefusal(err); ok {
		return refused
	}
	if err != nil {
		return down("consultando %s: %v", server, err)
	}

	values := answerValues(resp.Answer)
	count, shown := len(resp.Answer), true
	if m.RestrictInternal {
		values, shown = restrictedAnswers(values, qtype, cfg.Resolver == "")
		count = len(values)
	}

	res := Result{
		LatencyMS: latency,
		Meta: map[string]string{
			"record_type":  typeName,
			"resolver":     server,
			"answer_count": strconv.Itoa(count),
		},
	}

	if resp.Rcode != dns.RcodeSuccess {
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("resolvedor respondeu %s", dns.RcodeToString[resp.Rcode])
		return res
	}

	if shown {
		res.Meta["answers"] = strings.Join(values, ", ")
	}

	if len(values) == 0 {
		// O nome existe mas não tem o registro pedido; para quem depende
		// dele, o serviço continua inalcançável.
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("nenhum registro %s para %s", typeName, m.Target)
		return res
	}

	if len(cfg.ExpectedValues) > 0 && !containsAny(values, cfg.ExpectedValues) {
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("resposta %v não contém nenhum dos valores esperados %v",
			values, cfg.ExpectedValues)
		if !shown {
			res.Message = fmt.Sprintf("a resposta não contém nenhum dos valores esperados %v", cfg.ExpectedValues)
		}
		return res
	}

	res.Status = domain.StatusUp
	return res
}

// restrictedAnswers recorta a resposta de um monitor restrito, salvo por
// conta com acesso só a alguns grupos, e diz se os valores podem
// aparecer na causa e nos detalhes da verificação.
//
// Sem resolvedor próprio, o monitor pergunta ao do sistema, que costuma
// conhecer a zona interna da rede do servidor (db.corp.internal, o
// metadado da nuvem). Exigir resolvedor externo resolveria, mas a tela
// não oferece o campo, e o monitor DNS deixaria de existir para essas
// contas. Em vez disso, o endereço interno sai da resposta como se não
// tivesse vindo — inclusive para a comparação com os valores esperados,
// que de outro modo confirmaria um palpite —, e os registros que não são
// endereço (CNAME, MX, NS, TXT) não aparecem: não há como dizer se
// apontam para dentro, e um TXT da zona interna pode ser qualquer coisa.
// A comparação com os valores esperados continua valendo para eles.
//
// Com resolvedor escolhido, ele foi conferido no cadastro e na conexão,
// fica fora da rede do servidor e só conhece o que é público; o recorte
// do endereço interno vale igual, por coerência com o resto.
func restrictedAnswers(values []string, qtype uint16, systemResolver bool) ([]string, bool) {
	out := values[:0:0]
	for _, v := range values {
		if ip, err := netip.ParseAddr(v); err == nil && isInternal(ip) {
			continue
		}
		out = append(out, v)
	}
	address := qtype == dns.TypeA || qtype == dns.TypeAAAA
	return out, address || !systemResolver
}

// systemResolver lê o primeiro servidor configurado no sistema.
//
// O UpWatch roda em contêiner Linux, onde resolv.conf é a fonte correta e
// já reflete o DNS da rede do contêiner. Em sistemas sem esse arquivo o
// erro pede a configuração explícita, em vez de assumir um resolvedor
// público e passar a medir a rede de outra pessoa.
func systemResolver() (string, error) {
	cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(cfg.Servers) == 0 {
		return "", fmt.Errorf(
			"checker: não foi possível ler o resolvedor do sistema; informe \"resolver\" na configuração do monitor")
	}

	port := cfg.Port
	if port == "" {
		port = defaultDNSPort
	}
	return net.JoinHostPort(cfg.Servers[0], port), nil
}

// systemResolverAddr é systemResolver; variável só para o teste apontar
// "o resolvedor do sistema" para um servidor DNS local.
var systemResolverAddr = systemResolver

// resolverAddr resolve o endereço do servidor a consultar.
func (c *DNSChecker) resolverAddr(cfg DNSConfig) (string, error) {
	if cfg.Resolver == "" {
		return systemResolverAddr()
	}
	if _, _, err := net.SplitHostPort(cfg.Resolver); err != nil {
		// Sem porta explícita, assume a padrão em vez de recusar.
		return net.JoinHostPort(cfg.Resolver, defaultDNSPort), nil
	}
	return cfg.Resolver, nil
}

// answerValues extrai o valor legível de cada registro.
func answerValues(answers []dns.RR) []string {
	out := make([]string, 0, len(answers))
	for _, rr := range answers {
		switch v := rr.(type) {
		case *dns.A:
			out = append(out, v.A.String())
		case *dns.AAAA:
			out = append(out, v.AAAA.String())
		case *dns.CNAME:
			out = append(out, v.Target)
		case *dns.MX:
			out = append(out, v.Mx)
		case *dns.NS:
			out = append(out, v.Ns)
		case *dns.TXT:
			out = append(out, strings.Join(v.Txt, ""))
		}
	}
	return out
}

func containsAny(values, wanted []string) bool {
	for _, v := range values {
		for _, w := range wanted {
			// A comparação ignora o ponto final: o operador digita
			// "mail.exemplo.com" e o protocolo devolve "mail.exemplo.com."
			if strings.EqualFold(strings.TrimSuffix(v, "."), strings.TrimSuffix(w, ".")) {
				return true
			}
		}
	}
	return false
}

func parseDNSConfig(raw json.RawMessage) (DNSConfig, error) {
	var cfg DNSConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração DNS inválida: %w", err)
	}
	return cfg, nil
}
