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
// conexão: o host do endereço HTTP, o do host:porta de TCP e TLS, o do
// ping e o resolvedor configurado num monitor DNS.
//
// O nome consultado por um monitor DNS não entra: o servidor pergunta por
// ele ao resolvedor, mas não se conecta a ele. DNS sem resolvedor usa o do
// sistema, que o operador escolheu ao instalar. Push não se conecta a
// nada.
//
// Mora aqui, e não em quem valida, porque é o checker que sabe ler o alvo
// de cada tipo; uma segunda leitura do formato divergiria da primeira.
func Destinations(m domain.Monitor) ([]string, error) {
	switch m.Type {
	case domain.MonitorHTTP:
		u, err := url.Parse(strings.TrimSpace(m.Target))
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("checker: endereço %q sem host", m.Target)
		}
		return []string{u.Hostname()}, nil
	case domain.MonitorTCP, domain.MonitorTLS:
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
	default:
		return nil, nil
	}
}

// sharedAddressSpace é o 100.64.0.0/10 da RFC 6598. Não é privado pela
// definição do Go, mas é rede de provedor, e é onde mora o serviço de
// metadados de algumas nuvens (100.100.100.200).
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// thisNetwork é o 0.0.0.0/8: em Linux, conectar a ele chega à própria
// máquina.
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// InternalAddr informa se o endereço, alcançado a partir do servidor, é a
// rede de dentro dele: a própria máquina (loopback e não especificado), a
// rede privada (RFC 1918 e ULA), o enlace local — onde fica o serviço de
// metadados da nuvem, que entrega credenciais a quem pergunta — e o espaço
// compartilhado de provedor.
func InternalAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() ||
		sharedAddressSpace.Contains(a) || thisNetwork.Contains(a)
}
