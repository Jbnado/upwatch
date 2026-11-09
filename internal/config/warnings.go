package config

import (
	"net"
	"net/netip"
	"strings"
)

// DeploymentWarnings aponta as combinações que sobem sem erro e expõem
// mais do que quem instalou imagina.
//
// Não são erros de configuração: a instalação funciona, e recusar a
// partida por causa delas quebraria quem já está no ar. São coisas que só
// quem opera pode decidir, e que não têm como ser decididas sem saber que
// existem — daí sair no log a cada partida, enquanto durarem.
func (c Config) DeploymentWarnings() []string {
	var avisos []string

	// /metrics sem token é aberto: publica nome, grupo, estado e latência
	// de todos os monitores, e com isso a topologia interna. Enquanto a
	// escuta é só loopback, nada disso sai da máquina; a partir do momento
	// em que o endereço é alcançável da rede, sai para quem chegar na
	// porta. O critério é o endereço, e não a existência de proxy reverso
	// na frente, porque o proxy é justamente o que costuma publicar a
	// porta sem filtrar o caminho.
	if strings.TrimSpace(c.MetricsToken) == "" && !listensOnLoopbackOnly(c.Listen) {
		avisos = append(avisos, "/metrics está aberto: sem "+envPrefix+"METRICS_TOKEN, "+
			"qualquer um que alcance "+c.Listen+" lê o nome, o grupo, o estado e a latência "+
			"de todos os monitores; defina o token e envie-o como Bearer na raspagem, "+
			"ou escute só em loopback")
	}

	// O endereço público em https diz que existe TLS em algum ponto do
	// caminho, mas o cookie de sessão sem Secure vai junto numa requisição
	// em http puro — por um link antigo, por um redirecionamento perdido
	// ou por quem esteja na mesma rede forçando uma. É a combinação que
	// entrega a sessão em claro numa instalação que se acha protegida.
	if !c.SecureCookies && strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.PublicURL)), "https://") {
		avisos = append(avisos, envPrefix+"PUBLIC_URL é https mas "+envPrefix+"SECURE_COOKIES "+
			"está desligado: o cookie de sessão não é marcado como Secure e seguiria "+
			"numa requisição em http puro; ligue-o se todo o acesso passa por https")
	}

	return avisos
}

// listensOnLoopbackOnly informa se o endereço de escuta só aceita conexão
// da própria máquina.
//
// Endereço sem máquina (":8080") e o curinga ("0.0.0.0", "::") aceitam de
// qualquer interface. O nome "localhost" conta como loopback porque é para
// isso que ele resolve em qualquer instalação razoável; qualquer outro
// nome não dá para resolver aqui sem consultar DNS na partida, então
// conta como alcançável. Endereço que não dá para interpretar também
// conta como alcançável: errar para o lado do aviso custa uma linha de
// log, errar para o outro cala justamente o caso exposto.
func listensOnLoopbackOnly(listen string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback()
}
