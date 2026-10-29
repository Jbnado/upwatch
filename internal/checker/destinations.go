package checker

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Destinations devolve os hosts aos quais o checker do monitor abre
// conexão: o host do endereço HTTP, o do host:porta de TCP, TLS, Redis,
// PostgreSQL e MySQL, o do ping e o resolvedor configurado num monitor
// DNS.
//
// O nome consultado por um monitor DNS não entra: o servidor pergunta por
// ele ao resolvedor, mas não se conecta a ele. DNS sem resolvedor usa o do
// sistema, que o operador escolheu ao instalar. Push não se conecta a
// nada.
//
// Mora aqui, e não em quem valida, porque é o checker que sabe ler o alvo
// de cada tipo; uma segunda leitura do formato divergiria da primeira.
//
// Tipo desconhecido é erro, e não "sem destino": quem confere a rede
// interna trataria o silêncio como permissão, e o tipo novo esquecido
// aqui abriria a rede do servidor sem ninguém ver.
func Destinations(m domain.Monitor) ([]string, error) {
	switch m.Type {
	case domain.MonitorHTTP:
		u, err := url.Parse(strings.TrimSpace(m.Target))
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("checker: endereço %q sem host", m.Target)
		}
		return []string{u.Hostname()}, nil
	case domain.MonitorTCP, domain.MonitorTLS,
		domain.MonitorRedis, domain.MonitorPostgres, domain.MonitorMySQL:
		host, _, err := net.SplitHostPort(strings.TrimSpace(m.Target))
		if err != nil {
			return nil, fmt.Errorf("checker: alvo %q fora do formato host:porta", m.Target)
		}
		return []string{host}, nil
	case domain.MonitorICMP:
		return []string{strings.TrimSpace(m.Target)}, nil
	case domain.MonitorDNS:
		cfg, err := parseDNSConfig(m.Config)
		if err != nil || cfg.Resolver == "" {
			return nil, err
		}
		host := cfg.Resolver
		if h, _, err := net.SplitHostPort(cfg.Resolver); err == nil {
			host = h
		}
		return []string{strings.Trim(host, "[]")}, nil
	case domain.MonitorPush:
		return nil, nil
	default:
		return nil, fmt.Errorf("checker: tipo de monitor %q sem destino declarado", m.Type)
	}
}

// internalPrefixes são as faixas que, além das que netip já classifica
// (loopback, privada, enlace local, multicast), chegam à rede de dentro
// do servidor ou não deveriam sair dele.
var internalPrefixes = []netip.Prefix{
	// 100.64.0.0/10, RFC 6598. Não é privado pela definição do Go, mas é
	// rede de provedor, e é onde mora o serviço de metadados de algumas
	// nuvens (100.100.100.200).
	netip.MustParsePrefix("100.64.0.0/10"),
	// 0.0.0.0/8: em Linux, conectar a ele chega à própria máquina.
	netip.MustParsePrefix("0.0.0.0/8"),
	// 192.0.0.0/24, atribuições de protocolo da IANA: é onde ficam o
	// tradutor de NAT64 local e o DS-Lite, que levam para dentro.
	netip.MustParsePrefix("192.0.0.0/24"),
	// 198.18.0.0/15 é de teste de equipamento de rede, e em muita rede
	// corporativa é usado como privado.
	netip.MustParsePrefix("198.18.0.0/15"),
	// 240.0.0.0/4 é reservado; inclui 255.255.255.255, o broadcast da
	// rede local.
	netip.MustParsePrefix("240.0.0.0/4"),
	// fec0::/10, site-local: obsoleto, mas ainda roteado como rede
	// interna onde foi configurado.
	netip.MustParsePrefix("fec0::/10"),
}

// embeddedIPv4Prefixes são as faixas IPv6 que carregam um IPv4 dentro do
// endereço, e que um tradutor no caminho converte de volta nele: NAT64
// (64:ff9b::/96 e 64:ff9b:1::/48), 6to4 (2002::/16), IPv4-compatível
// (::/96) e SIIT (::ffff:0:0:0/96). Num servidor atrás de NAT64,
// [64:ff9b::a00:5] é 10.0.0.5.
//
// Recusadas por inteiro, sem extrair o IPv4 de dentro: cada formato põe o
// IPv4 num lugar, um tradutor pode usar prefixo próprio, e errar a
// extração abriria a rede interna. Um site público alcançável só por
// essas faixas não existe na prática; quem tem NAT64 resolve o nome para
// o IPv4 e para o IPv6 nativo do destino.
var embeddedIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
}

// InternalAddr informa se o endereço, alcançado a partir do servidor, é a
// rede de dentro dele: a própria máquina (loopback e não especificado), a
// rede privada (RFC 1918 e ULA), o enlace local — onde fica o serviço de
// metadados da nuvem, que entrega credenciais a quem pergunta —, o espaço
// compartilhado de provedor, as faixas reservadas e os endereços IPv6
// que embutem um IPv4.
func InternalAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return true
	}
	for _, p := range internalPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	for _, p := range embeddedIPv4Prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
