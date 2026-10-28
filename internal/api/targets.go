package api

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Alvos na rede interna do servidor.
//
// O checker se conecta a partir do servidor. Uma conta com escopo que
// cadastra monitor para 127.0.0.1, para a rede privada ou para o serviço
// de metadados da nuvem usa o servidor como sonda na rede de dentro dele:
// varre portas lendo a causa da falha de cada uma e, no metadado, pode
// sair com credencial. Quem alcança tudo já administra o servidor, e por
// isso a regra vale só para conta restrita — e pode ser desligada, porque
// há instalação que existe justamente para isso.
//
// A conferência é na validação, resolvendo o nome na hora. O checker ainda
// não confere ao conectar: um nome que troca de endereço depois do
// cadastro (DNS rebinding) e um redirecionamento HTTP para dentro passam.
// Fechar isso exige guardar no monitor a política que vale para ele e
// levar um controle de dialer a cada checker (issue #59).

// Resolver resolve nomes em endereços. *net.Resolver atende; o teste
// injeta um que não depende da rede.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// resolveTimeout limita a resolução feita durante o cadastro. Um
// resolvedor travado não pode segurar a requisição de quem salva.
const resolveTimeout = 5 * time.Second

// checkTargetReach recusa, para conta restrita, o monitor cujo destino
// resolva para a rede interna do servidor. anterior é nulo na criação.
//
// Na edição, o destino que não mudou não é conferido de novo: alguém de
// escopo total pode ter apontado o monitor para dentro, e quem tem o
// grupo continua podendo pausá-lo ou renomeá-lo. Mudar o alvo, mesmo só a
// porta, é escolher um destino novo, e esse é conferido.
func (a *API) checkTargetReach(ctx context.Context, v viewer, m domain.Monitor, anterior *domain.Monitor) error {
	if v.access.All() || a.allowScopedPrivateTargets {
		return nil
	}
	campo := "target"
	if m.Type == domain.MonitorDNS {
		campo = "config"
	}
	destinos, err := checker.Destinations(m)
	if err != nil {
		return &domain.ValidationError{Field: campo, Msg: err.Error()}
	}
	if anterior != nil && sameDestination(*anterior, m, destinos) {
		return nil
	}

	for _, host := range destinos {
		enderecos, err := a.lookup(ctx, host)
		if err != nil || len(enderecos) == 0 {
			return &domain.ValidationError{Field: campo, Msg: fmt.Sprintf(
				"não foi possível resolver %q; sem isso não há como confirmar que o alvo não é a rede interna do servidor", host)}
		}
		for _, e := range enderecos {
			if checker.InternalAddr(e) {
				return &domain.ValidationError{Field: campo, Msg: fmt.Sprintf(
					"%q aponta para a rede interna do servidor (%s); contas com acesso restrito a grupos não monitoram endereços internos",
					host, e)}
			}
		}
	}
	return nil
}

// sameDestination informa se a edição mantém o destino do monitor.
func sameDestination(anterior, m domain.Monitor, destinos []string) bool {
	if anterior.Type != m.Type || strings.TrimSpace(anterior.Target) != strings.TrimSpace(m.Target) {
		return false
	}
	antes, err := checker.Destinations(anterior)
	return err == nil && slices.Equal(antes, destinos)
}

// lookup devolve os endereços do host, sem consultar DNS quando ele já é
// um endereço.
func (a *API) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.WithZone("")}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	return a.resolver.LookupNetIP(ctx, "ip", host)
}
