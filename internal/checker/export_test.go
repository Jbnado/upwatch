package checker

import (
	"net"
	"net/netip"
	"testing"
)

// SetNetworkForTest troca, enquanto o teste roda, o que conta como rede
// interna e o resolvedor usado pelos dialers dos checkers. São variáveis
// do pacote: o teste que a usa não pode rodar em paralelo.
func SetNetworkForTest(t testing.TB, internal func(netip.Addr) bool, r *net.Resolver) {
	t.Helper()
	oldInternal, oldResolver := isInternal, lookupResolver
	isInternal, lookupResolver = internal, r
	t.Cleanup(func() { isInternal, lookupResolver = oldInternal, oldResolver })
}

// SetSystemResolverForTest faz do servidor dado o resolvedor do sistema
// do monitor DNS sem resolvedor próprio, enquanto o teste roda.
func SetSystemResolverForTest(t testing.TB, addr string) {
	t.Helper()
	old := systemResolverAddr
	systemResolverAddr = func() (string, error) { return addr, nil }
	t.Cleanup(func() { systemResolverAddr = old })
}

// RefuseInternalForTest expõe o Control do dialer restrito, para conferir
// a recusa de endereços que o teste não consegue discar de verdade.
var RefuseInternalForTest = refuseInternal
