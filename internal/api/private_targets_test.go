package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
)

// Alvos na rede interna do servidor.
//
// O checker se conecta a partir do servidor. Um administrador restrito a
// um grupo que cadastra monitor para 127.0.0.1, 10.0.0.0/8 ou
// 169.254.169.254 usa o servidor como sonda na rede de dentro dele: varre
// portas, lê a causa da falha de cada uma e, no metadado da nuvem, pode
// sair com credencial. Quem alcança tudo já administra o servidor, e para
// essa conta nada muda.

// resolvedorFalso responde sem rede: o teste não pode depender do DNS de
// quem o roda, e o nome "interno" precisa resolver para onde o teste diz.
type resolvedorFalso map[string][]netip.Addr

var errNaoResolve = errors.New("nome não resolve")

func (r resolvedorFalso) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ips, ok := r[host]; ok {
		if len(ips) == 0 {
			return nil, errNaoResolve
		}
		return ips, nil
	}
	return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
}

var resolvedorDosTestes = resolvedorFalso{
	"interno.exemplo": {netip.MustParseAddr("192.168.1.10")},
	// Um nome com um endereço de fora e um de dentro: basta um interno
	// para recusar, porque o checker pode cair em qualquer um.
	"misto.exemplo":       {netip.MustParseAddr("203.0.113.20"), netip.MustParseAddr("10.0.0.9")},
	"nao-resolve.exemplo": {},
}

func monitorPara(nome, tipo, alvo string, grupo int64) map[string]any {
	corpo := monitorNoGrupo(nome, &grupo)
	corpo["type"] = tipo
	corpo["target"] = alvo
	return corpo
}

func TestScopedAdminCannotTargetTheInternalNetwork(t *testing.T) {
	c := novoCenarioEscopo(t)
	adm := c.s.como(c.retAdmin)

	recusados := []struct{ nome, tipo, alvo string }{
		{"loopback", "http", "http://127.0.0.1:8080/admin"},
		{"loopback ipv6", "http", "http://[::1]/"},
		{"rede privada", "tcp", "10.0.0.5:22"},
		{"metadado da nuvem", "http", "http://169.254.169.254/latest/meta-data/"},
		{"ula", "tls", "[fd00::1]:443"},
		{"ping interno", "icmp", "192.168.0.1"},
		{"nome que resolve para dentro", "http", "https://interno.exemplo/"},
		{"nome com um endereço interno", "tcp", "misto.exemplo:443"},
		// Sem resolver, não há como saber para onde o checker vai.
		{"nome que não resolve", "http", "https://nao-resolve.exemplo/"},
	}
	for _, caso := range recusados {
		t.Run(caso.nome, func(t *testing.T) {
			resp := adm.do(t, http.MethodPost, "/api/v1/monitors", monitorPara("sonda "+caso.nome, caso.tipo, caso.alvo, c.retProd))
			assertStatus(t, resp, http.StatusBadRequest)
		})
	}

	t.Run("resolvedor dns interno", func(t *testing.T) {
		corpo := monitorPara("dns", "dns", "exemplo.com", c.retProd)
		corpo["config"] = map[string]any{"resolver": "10.0.0.53"}
		assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/monitors", corpo), http.StatusBadRequest)
	})

	// O monitor de banco entra com usuário e senha: apontado para o banco
	// do próprio servidor, a conta restrita testaria credenciais contra
	// ele e leria a causa de cada recusa. A configuração vai válida, e o
	// mesmo corpo com alvo de fora passa, para a recusa ser pelo alvo.
	for _, caso := range []struct{ nome, tipo, alvo string }{
		{"redis local", "redis", "127.0.0.1:6379"},
		{"postgres na rede privada", "postgres", "10.0.0.7:5432"},
		{"mysql por nome interno", "mysql", "interno.exemplo:3306"},
	} {
		t.Run(caso.nome, func(t *testing.T) {
			corpo := monitorPara("banco "+caso.nome, caso.tipo, caso.alvo, c.retProd)
			corpo["config"] = map[string]any{"username": "monitor", "password": "senha-do-banco"}
			assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/monitors", corpo), http.StatusBadRequest)

			corpo["name"] = "banco de fora " + caso.nome
			corpo["target"] = "banco.publico.exemplo:5432"
			assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/monitors", corpo), http.StatusCreated)
		})
	}

	t.Run("editar para alvo interno", func(t *testing.T) {
		resp := adm.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(c.dentro),
			monitorPara("bff", "http", "http://127.0.0.1/", c.retProd))
		assertStatus(t, resp, http.StatusBadRequest)
	})

	// Controle positivo: o alvo de fora continua permitido.
	assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/monitors",
		monitorPara("publico", "http", "https://publico.exemplo/saude", c.retProd)), http.StatusCreated)
}

// Quem alcança tudo administra o servidor, e monitorar a rede interna é
// justamente o uso de muita instalação caseira.
func TestFullScopeAdminCanTargetTheInternalNetwork(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	g := s.criarGrupo(t, "Infra", nil)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/monitors",
		monitorPara("roteador", "http", "http://192.168.0.1/", g)), http.StatusCreated)
}

// O monitor que alguém de escopo total apontou para dentro continua
// editável por quem tem o grupo — pausar, renomear —, desde que o destino
// não mude. Recusar travaria o monitor de quem não escolheu o alvo.
func TestScopedAdminKeepsAnInternalTargetChosenByOthers(t *testing.T) {
	c := novoCenarioEscopo(t)

	id := c.s.como(c.admin).createMonitor(t, monitorPara("banco", "tcp", "10.0.0.7:5432", c.retProd))

	corpo := monitorPara("banco-renomeado", "tcp", "10.0.0.7:5432", c.retProd)
	corpo["enabled"] = false
	assertStatus(t, c.s.como(c.retAdmin).do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), corpo), http.StatusOK)

	assertStatus(t, c.s.como(c.retAdmin).do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id),
		monitorPara("banco", "tcp", "10.0.0.8:5432", c.retProd)), http.StatusBadRequest)
}

// Desligada a restrição (UPWATCH_RESTRICT_PRIVATE_TARGETS=false), a conta
// restrita monitora a rede interna: é a escolha de quem opera um servidor
// que existe para isso.
func TestPrivateTargetsAllowedWhenRestrictionIsOff(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.AllowScopedPrivateTargets = true })
	s.setup(t)
	g := s.criarGrupo(t, "Retaguarda", nil)
	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "ret", "password": "senha-de-teste-12345", "role": "admin",
		"scope": map[string]any{"all_groups": false, "group_ids": []int64{g}},
	})
	assertStatus(t, resp, http.StatusCreated)
	adm := s.como(s.entrar(t, "ret", "senha-de-teste-12345"))

	assertStatus(t, adm.do(t, http.MethodPost, "/api/v1/monitors",
		monitorPara("banco", "tcp", "10.0.0.7:5432", g)), http.StatusCreated)
}
