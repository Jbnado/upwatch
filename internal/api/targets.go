package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
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
// A conferência acontece duas vezes. Aqui, na validação, resolvendo o nome
// na hora, para o erro aparecer no cadastro. E no checker, ao conectar,
// com o endereço efetivamente conectado: um nome que troca de endereço
// depois do cadastro (DNS rebinding) e um redirecionamento HTTP para
// dentro só aparecem ali. O checker não sabe quem salvou o monitor; a
// marca RestrictInternal, decidida em restrictInternal, é como ele fica
// sabendo. As duas usam checker.InternalAddr, para não divergirem.

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
// porta, é escolher um destino novo, e esse é conferido. Mudar o que vai
// na requisição também (ver sameDestination).
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

// restrictInternal decide a marca RestrictInternal do monitor que vai ser
// gravado. anterior é nulo na criação.
//
// A marca segue quem escolheu o destino, a mesma regra de
// checkTargetReach: a conta restrita que só renomeia ou pausa o monitor
// que alguém de escopo total apontou para dentro não o marca, senão ele
// cairia na verificação seguinte por um destino que ela nem escolheu.
// Quem alcança tudo, ao salvar, assume o destino e tira a marca.
func (a *API) restrictInternal(v viewer, m domain.Monitor, anterior *domain.Monitor) bool {
	if v.access.All() || a.allowScopedPrivateTargets {
		return false
	}
	if anterior != nil {
		if destinos, err := checker.Destinations(m); err == nil && sameDestination(*anterior, m, destinos) {
			return anterior.RestrictInternal
		}
	}
	return true
}

// sameDestination informa se a edição mantém o destino do monitor.
//
// Destino é o alvo e tudo o que molda o que vai até ele: método, corpo,
// cabeçalhos, autenticação, asserções sobre a resposta, a consulta e o
// usuário do banco. Manter o endereço e trocar o resto não é "o mesmo
// destino": transformaria a verificação de saúde que alguém de escopo
// total apontou para dentro numa requisição escolhida pela conta
// restrita, que muda estado no serviço interno e lê a resposta pelas
// asserções. Toda a configuração mora em Config, então basta compará-la;
// nome, pausa, intervalo, tempo limite, limiares, grupo e etiquetas ficam
// fora dela e continuam livres.
func sameDestination(anterior, m domain.Monitor, destinos []string) bool {
	if anterior.Type != m.Type || strings.TrimSpace(anterior.Target) != strings.TrimSpace(m.Target) {
		return false
	}
	if !sameConfig(anterior.Config, m.Config) {
		return false
	}
	antes, err := checker.Destinations(anterior)
	return err == nil && slices.Equal(antes, destinos)
}

// sameConfig compara duas configurações pelo conteúdo, sem olhar ordem de
// chaves nem espaços. Ausente, null e objeto vazio são a mesma coisa: a
// configuração padrão. A edição já chega com a senha guardada de volta
// (carrySecrets), então reenviar o formulário sem ela não conta como
// mudança.
func sameConfig(a, b json.RawMessage) bool {
	va, errA := decodeConfig(a)
	vb, errB := decodeConfig(b)
	return errA == nil && errB == nil && reflect.DeepEqual(va, vb)
}

func decodeConfig(raw json.RawMessage) (any, error) {
	var v any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
	}
	if obj, ok := v.(map[string]any); v == nil || ok && len(obj) == 0 {
		return map[string]any{}, nil
	}
	return v, nil
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
