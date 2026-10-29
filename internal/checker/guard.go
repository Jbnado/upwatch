package checker

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"

	"github.com/Jbnado/upwatch/internal/domain"
)

// ErrInternalTarget é a recusa de conexão de um monitor com
// RestrictInternal a um endereço da rede interna do servidor.
//
// O texto não traz o endereço: a causa é lida por quem cadastrou o
// monitor, e dizer para onde o nome resolveu do lado de dentro entregaria
// o DNS interno do servidor a uma conta que só tem um grupo.
var ErrInternalTarget = errors.New("o destino aponta para a rede interna do servidor; " +
	"monitores de contas com acesso restrito a grupos não alcançam endereços internos")

// isInternal decide o que é rede interna na conexão. É InternalAddr, a
// mesma função da validação do cadastro, para as duas não divergirem; a
// variável existe só para o teste fazer de um endereço de loopback um
// endereço "de fora" e simular rebinding e redirecionamento sem rede.
var isInternal = InternalAddr

// lookupResolver resolve os nomes nos dialers dos checkers. Nulo usa o
// resolvedor do sistema; o teste injeta um que troca de resposta.
var lookupResolver *net.Resolver

// refuseInternal é o Control do dialer de um monitor restrito.
//
// Roda depois da resolução e antes do connect, com o endereço que vai ser
// de fato conectado. Conferir o nome antes de discar deixaria a janela
// entre a conferência e a conexão — justamente onde o rebinding troca a
// resposta. Endereço que não se lê como IP é recusado: o dialer sempre
// entrega IP aqui, e o inesperado não pode virar permissão.
func refuseInternal(_ context.Context, _, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrInternalTarget
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || isInternal(ip) {
		return ErrInternalTarget
	}
	return nil
}

// dialerFor devolve o dialer das conexões do monitor.
//
// Sem a restrição é o dialer de sempre: o monitor do administrador de
// escopo total e a sonda, que roda dentro da rede privada justamente para
// alcançá-la, não mudam de comportamento.
func dialerFor(m domain.Monitor) *net.Dialer {
	d := &net.Dialer{Resolver: lookupResolver}
	if m.RestrictInternal {
		d.ControlContext = refuseInternal
	}
	return d
}

// internalRefusal traduz a recusa na causa da verificação, sem o
// endereço que as bibliotecas embrulham junto no erro.
func internalRefusal(err error) (Result, bool) {
	if !errors.Is(err, ErrInternalTarget) {
		return Result{}, false
	}
	return Result{Status: domain.StatusDown, Message: ErrInternalTarget.Error()}, true
}

// checkedAddr resolve o host de um monitor restrito e devolve o endereço
// conferido, para quem não conecta por um dialer (o ping). Quem usa o
// endereço devolvido não resolve o nome de novo: resolver outra vez
// reabriria a janela de rebinding.
func checkedAddr(ctx context.Context, host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if isInternal(ip) {
			return "", ErrInternalTarget
		}
		return ip.String(), nil
	}
	r := lookupResolver
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if !isInternal(a) {
			return a.Unmap().String(), nil
		}
	}
	return "", ErrInternalTarget
}
