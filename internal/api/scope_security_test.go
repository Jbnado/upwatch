package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/api"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Vazamento entre escopos.
//
// Quem tem escopo "Retaguarda" não pode chegar, por endpoint nenhum, a um
// monitor do RPA — nem saber que ele existe. Cada teste aqui é escrito do
// ponto de vista dessa pessoa, com a credencial dela na mão, tentando
// enxergar ou mexer no que é dos outros.
//
// A tabela percorre todas as rotas que o roteador atende. Rota nova sem
// classificação reprova o teste: a pergunta "isto respeita o escopo?" tem
// de ser respondida por quem cria a rota, não descoberta por quem a
// explora.

// segredos são os textos que só existem fora do escopo. Qualquer um deles
// numa resposta é vazamento.
var segredos = []string{"segredo-rpa", "segredo-solto", "SistemaSecreto", "ambiente-secreto", "mensagem-secreta", "outro-total", "pager-diretoria"}

// cenarioEscopo é a instalação de referência da issue: dois sistemas, e
// contas restritas a um deles.
type cenarioEscopo struct {
	s *server

	admin, retViewer, retAdmin *http.Cookie

	rpa, rpaProd, ret, retProd int64

	// foraAgrupado está num grupo que a conta restrita não alcança;
	// foraSolto não tem grupo, e "Sem grupo" só é visível ao escopo total.
	foraAgrupado, foraSolto, dentro int64

	contaTotal, contaRestrita int64
	pagina, relato            int64
	// canal só avisa sobre o que está fora do escopo; canalDentro avisa
	// sobre o grupo da Retaguarda.
	canal, canalDentro int64
}

func (s *server) como(c *http.Cookie) *server {
	cp := *s
	cp.cookie = c
	return &cp
}

func (s *server) entrar(t *testing.T, usuario, senha string) *http.Cookie {
	t.Helper()
	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": usuario, "password": senha,
	})
	assertStatus(t, resp, http.StatusOK)
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookieName {
			return c
		}
	}
	t.Fatal("login sem cookie")
	return nil
}

// criarGrupo cadastra um grupo com a sessão atual e devolve o id.
func (s *server) criarGrupo(t *testing.T, nome string, pai *int64) int64 {
	t.Helper()
	corpo := map[string]any{"name": nome}
	if pai != nil {
		corpo["parent_id"] = *pai
	}
	resp := s.do(t, http.MethodPost, "/api/v1/groups", corpo)
	assertStatus(t, resp, http.StatusCreated)
	return decode[idResp](t, resp).ID
}

func monitorNoGrupo(nome string, grupo *int64) map[string]any {
	corpo := newMonitorBody(nome)
	if grupo != nil {
		corpo["group_id"] = *grupo
	}
	return corpo
}

func novoCenarioEscopo(t *testing.T) *cenarioEscopo {
	t.Helper()

	s := newServer(t)
	s.setup(t)
	c := &cenarioEscopo{s: s, admin: s.cookie}
	ctx := context.Background()

	c.rpa = s.criarGrupo(t, "SistemaSecreto", nil)
	c.rpaProd = s.criarGrupo(t, "ambiente-secreto", &c.rpa)
	c.ret = s.criarGrupo(t, "Retaguarda", nil)
	c.retProd = s.criarGrupo(t, "prod", &c.ret)

	c.foraAgrupado = s.createMonitor(t, monitorNoGrupo("segredo-rpa", &c.rpaProd))
	c.foraSolto = s.createMonitor(t, monitorNoGrupo("segredo-solto", nil))
	c.dentro = s.createMonitor(t, monitorNoGrupo("bff", &c.retProd))

	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "pager-diretoria", "type": "webhook", "config": map[string]any{"url": "https://exemplo.com/hook"},
	})
	assertStatus(t, resp, http.StatusCreated)
	c.canal = decode[idResp](t, resp).ID
	resp = s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "aviso-retaguarda", "type": "webhook", "config": map[string]any{"url": "https://exemplo.com/ret"},
	})
	assertStatus(t, resp, http.StatusCreated)
	c.canalDentro = decode[idResp](t, resp).ID

	// Histórico do que está fora: batidas, mudança de estado e incidente,
	// para as listagens terem o que vazar.
	for _, id := range []int64{c.foraAgrupado, c.foraSolto, c.dentro} {
		causa := "mensagem-secreta"
		if id == c.dentro {
			causa = "falha-visivel"
		}
		if err := s.store.WriteHeartbeats(ctx, []domain.Heartbeat{{
			MonitorID: id, Timestamp: epoch.Add(-time.Minute), Status: domain.StatusDown,
			Message: causa, ProbeID: "local",
		}}); err != nil {
			t.Fatal(err)
		}
		if err := s.store.States().Save(ctx, id, domain.MonitorState{
			Status: domain.StatusDown, Candidate: domain.StatusDown, Since: epoch.Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		inc := domain.Incident{MonitorID: id, StartedAt: epoch.Add(-time.Hour), Cause: causa}
		if err := s.store.Incidents().Open(ctx, &inc); err != nil {
			t.Fatal(err)
		}
		tr := domain.Transition{MonitorID: id, From: domain.StatusUp, To: domain.StatusDown,
			At: epoch.Add(-time.Hour), Cause: causa}
		if err := s.store.Transitions().Record(ctx, &tr); err != nil {
			t.Fatal(err)
		}
	}
	for _, rota := range []string{
		"/api/v1/monitors/" + itoa(c.foraAgrupado) + "/channels/" + itoa(c.canal),
		"/api/v1/groups/" + itoa(c.rpa) + "/channels/" + itoa(c.canal),
		"/api/v1/groups/" + itoa(c.retProd) + "/channels/" + itoa(c.canalDentro),
	} {
		assertStatus(t, s.do(t, http.MethodPut, rota, nil), http.StatusNoContent)
	}

	resp = s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{"slug": "estado", "title": "Estado"})
	assertStatus(t, resp, http.StatusCreated)
	c.pagina = decode[idResp](t, resp).ID
	for _, id := range []int64{c.foraAgrupado, c.dentro} {
		assertStatus(t, s.do(t, http.MethodPut,
			"/api/v1/status-pages/"+itoa(c.pagina)+"/components/"+itoa(id),
			map[string]any{"label": "componente"}), http.StatusOK)
	}

	resp = s.do(t, http.MethodPost, "/api/v1/announcements", map[string]any{
		"title": "Falha", "impact": "major", "phase": "investigating",
		"components": []int64{c.foraAgrupado, c.dentro},
	})
	assertStatus(t, resp, http.StatusCreated)
	c.relato = decode[idResp](t, resp).ID

	escopoRet := map[string]any{"all_groups": false, "group_ids": []int64{c.ret}}
	for _, conta := range []struct{ nome, papel string }{{"ret-viewer", "viewer"}, {"ret-admin", "admin"}} {
		resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
			"username": conta.nome, "password": "senha-de-teste-12345", "role": conta.papel,
			"scope": escopoRet,
		})
		assertStatus(t, resp, http.StatusCreated)
		if conta.nome == "ret-admin" {
			c.contaRestrita = decode[idResp](t, resp).ID
		}
	}
	resp = s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "outro-total", "password": "senha-de-teste-12345", "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)
	c.contaTotal = decode[idResp](t, resp).ID

	c.retViewer = s.entrar(t, "ret-viewer", "senha-de-teste-12345")
	c.retAdmin = s.entrar(t, "ret-admin", "senha-de-teste-12345")
	return c
}

// classe diz como uma rota se relaciona com o escopo.
type classe int

const (
	// publica não exige credencial; as regras dela estão em
	// public_security_test.go e metrics_test.go.
	publica classe = iota
	// semRecurso não diz respeito a monitor nem a grupo: a própria conta,
	// os tipos de canal.
	semRecurso
	// porMonitor tem um monitor no caminho ({id} sob /monitors ou
	// {monitorID}). Fora do escopo responde 404.
	porMonitor
	// porGrupo tem um grupo no caminho. Fora do escopo responde 404.
	porGrupo
	// porCanal tem um canal no caminho. Canal que não avisa sobre nada do
	// escopo responde 404: o nome e o destino dele dizem quem existe fora.
	porCanal
	// porConta tem uma conta no caminho. Conta com escopo maior que o de
	// quem pede responde 404.
	porConta
	// listagem devolve coleção ou recurso global; o corpo não pode citar
	// nada de fora do escopo.
	listagem
	// escopoTotal altera recurso global (canal, página pública, relato),
	// inclusive o vínculo de canal com monitor ou grupo: o canal é de todo
	// mundo, e pendurá-lo no próprio grupo é mexer no pager dos outros.
	// Responde 403 a quem tem escopo, sem olhar o recurso — a resposta é
	// a mesma para monitor de dentro e de fora, e não confirma nada.
	escopoTotal
	// criacao cria algo dentro de um grupo; a guarda contra escalonamento
	// é testada à parte.
	criacao
	// fluxo é o SSE, testado à parte.
	fluxo
)

// rotas classifica cada rota atendida pelo roteador.
var rotas = map[string]classe{
	"GET /healthz":                                            publica,
	"GET /metrics":                                            publica,
	"GET /api/v1/openapi.yaml":                                publica,
	"GET /api/v1/setup":                                       publica,
	"POST /api/v1/setup":                                      publica,
	"POST /api/v1/auth/login":                                 publica,
	"GET /api/v1/push/{token}":                                publica,
	"POST /api/v1/push/{token}":                               publica,
	"GET /api/v1/public":                                      publica,
	"GET /api/v1/public/feed.atom":                            publica,
	"GET /api/v1/public/{slug}":                               publica,
	"GET /api/v1/public/{slug}/feed.atom":                     publica,
	"POST /api/v1/auth/logout":                                semRecurso,
	"GET /api/v1/auth/me":                                     semRecurso,
	"POST /api/v1/auth/password":                              semRecurso,
	"GET /api/v1/auth/tokens":                                 semRecurso,
	"POST /api/v1/auth/tokens":                                semRecurso,
	"DELETE /api/v1/auth/tokens/{id}":                         semRecurso,
	"GET /api/v1/channel-types":                               semRecurso,
	"GET /api/v1/events":                                      fluxo,
	"GET /api/v1/summaries":                                   listagem,
	"GET /api/v1/monitors":                                    listagem,
	"GET /api/v1/channels":                                    listagem,
	"GET /api/v1/channels/{id}":                               porCanal,
	"GET /api/v1/incidents":                                   listagem,
	"GET /api/v1/status-pages":                                listagem,
	"GET /api/v1/status-pages/{id}":                           listagem,
	"GET /api/v1/announcements":                               listagem,
	"GET /api/v1/announcements/{id}":                          listagem,
	"GET /api/v1/groups":                                      listagem,
	"GET /api/v1/groups/summary":                              listagem,
	"GET /api/v1/problems":                                    listagem,
	"GET /api/v1/users":                                       listagem,
	"GET /api/v1/monitors/{id}":                               porMonitor,
	"PUT /api/v1/monitors/{id}":                               porMonitor,
	"DELETE /api/v1/monitors/{id}":                            porMonitor,
	"POST /api/v1/monitors/{id}/children":                     porMonitor,
	"DELETE /api/v1/monitors/{id}/children/{childID}":         porMonitor,
	"GET /api/v1/monitors/{id}/heartbeats":                    porMonitor,
	"GET /api/v1/monitors/{id}/rollups":                       porMonitor,
	"GET /api/v1/monitors/{id}/export":                        porMonitor,
	"GET /api/v1/monitors/{id}/events":                        porMonitor,
	"GET /api/v1/monitors/{id}/channels":                      porMonitor,
	"GET /api/v1/groups/{id}":                                 porGrupo,
	"PUT /api/v1/groups/{id}":                                 porGrupo,
	"DELETE /api/v1/groups/{id}":                              porGrupo,
	"GET /api/v1/groups/{id}/channels":                        porGrupo,
	"PUT /api/v1/users/{id}":                                  porConta,
	"DELETE /api/v1/users/{id}":                               porConta,
	"POST /api/v1/monitors":                                   criacao,
	"POST /api/v1/groups":                                     criacao,
	"POST /api/v1/users":                                      criacao,
	"POST /api/v1/channels":                                   escopoTotal,
	"PUT /api/v1/monitors/{id}/channels/{channelID}":          escopoTotal,
	"DELETE /api/v1/monitors/{id}/channels/{channelID}":       escopoTotal,
	"PUT /api/v1/groups/{id}/channels/{channelID}":            escopoTotal,
	"DELETE /api/v1/groups/{id}/channels/{channelID}":         escopoTotal,
	"PUT /api/v1/channels/{id}":                               escopoTotal,
	"DELETE /api/v1/channels/{id}":                            escopoTotal,
	"POST /api/v1/channels/{id}/test":                         escopoTotal,
	"POST /api/v1/status-pages":                               escopoTotal,
	"PUT /api/v1/status-pages/{id}":                           escopoTotal,
	"DELETE /api/v1/status-pages/{id}":                        escopoTotal,
	"PUT /api/v1/status-pages/{id}/default":                   escopoTotal,
	"POST /api/v1/status-pages/{id}/groups":                   escopoTotal,
	"PUT /api/v1/status-pages/{id}/groups/{groupID}":          escopoTotal,
	"DELETE /api/v1/status-pages/{id}/groups/{groupID}":       escopoTotal,
	"PUT /api/v1/status-pages/{id}/components/{monitorID}":    escopoTotal,
	"DELETE /api/v1/status-pages/{id}/components/{monitorID}": escopoTotal,
	"POST /api/v1/announcements":                              escopoTotal,
	"PUT /api/v1/announcements/{id}":                          escopoTotal,
	"DELETE /api/v1/announcements/{id}":                       escopoTotal,
	"POST /api/v1/announcements/{id}/updates":                 escopoTotal,
}

// TestEveryRouteHasAScopeClass reprova rota sem classificação.
//
// É o que impede uma rota nova de nascer sem ninguém ter perguntado se
// ela respeita o escopo.
func TestEveryRouteHasAScopeClass(t *testing.T) {
	var faltando []string
	for padrao, metodos := range registeredRoutes(t) {
		for _, metodo := range metodos {
			chave := strings.ToUpper(metodo) + " " + padrao
			if _, ok := rotas[chave]; !ok {
				faltando = append(faltando, chave)
			}
		}
	}
	sort.Strings(faltando)
	if len(faltando) > 0 {
		t.Fatalf("rotas sem classe de escopo em scope_security_test.go: %v", faltando)
	}
}

// concreto troca os parâmetros do padrão pelos ids do cenário.
func (c *cenarioEscopo) concreto(padrao string, monitor, grupo int64) string {
	caminho := padrao
	troca := func(param string, v int64) { caminho = strings.ReplaceAll(caminho, param, itoa(v)) }

	switch {
	case strings.HasPrefix(padrao, "/api/v1/monitors/{id}"):
		troca("{id}", monitor)
	case strings.HasPrefix(padrao, "/api/v1/groups/{id}"):
		troca("{id}", grupo)
	case strings.HasPrefix(padrao, "/api/v1/status-pages/{id}"):
		troca("{id}", c.pagina)
	case strings.HasPrefix(padrao, "/api/v1/announcements/{id}"):
		troca("{id}", c.relato)
	case strings.HasPrefix(padrao, "/api/v1/channels/{id}"):
		troca("{id}", c.canal)
	case strings.HasPrefix(padrao, "/api/v1/users/{id}"):
		troca("{id}", c.contaTotal)
	}
	troca("{channelID}", c.canal)
	troca("{monitorID}", monitor)
	troca("{childID}", monitor)
	troca("{groupID}", 1)
	return caminho
}

func separar(chave string) (string, string) {
	metodo, padrao, _ := strings.Cut(chave, " ")
	return metodo, padrao
}

// TestScopedAccountGets404OutsideItsScope percorre toda rota com monitor
// ou grupo no caminho.
//
// 404, e nunca 403: responder "proibido" para um monitor de outro grupo e
// "não encontrado" para um id inventado ensinaria a quem chuta ids quais
// existem.
func TestScopedAccountGets404OutsideItsScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	for chave, cl := range rotas {
		metodo, padrao := separar(chave)
		switch cl {
		case porMonitor:
			for nome, id := range map[string]int64{"agrupado": c.foraAgrupado, "sem grupo": c.foraSolto} {
				for conta, cookie := range map[string]*http.Cookie{"observador": c.retViewer, "admin": c.retAdmin} {
					if metodo != http.MethodGet && conta == "observador" {
						continue
					}
					t.Run(chave+" "+nome+" "+conta, func(t *testing.T) {
						resp := c.s.como(cookie).do(t, metodo, c.concreto(padrao, id, c.rpa), map[string]any{})
						assertStatus(t, resp, http.StatusNotFound)
					})
				}
			}
		case porGrupo:
			for nome, id := range map[string]int64{"sistema": c.rpa, "ambiente": c.rpaProd} {
				for conta, cookie := range map[string]*http.Cookie{"observador": c.retViewer, "admin": c.retAdmin} {
					if metodo != http.MethodGet && conta == "observador" {
						continue
					}
					t.Run(chave+" "+nome+" "+conta, func(t *testing.T) {
						resp := c.s.como(cookie).do(t, metodo, c.concreto(padrao, c.dentro, id), map[string]any{})
						assertStatus(t, resp, http.StatusNotFound)
					})
				}
			}
		case porCanal:
			for conta, cookie := range map[string]*http.Cookie{"observador": c.retViewer, "admin": c.retAdmin} {
				t.Run(chave+" "+conta, func(t *testing.T) {
					resp := c.s.como(cookie).do(t, metodo, c.concreto(padrao, 0, 0), nil)
					assertStatus(t, resp, http.StatusNotFound)
				})
			}
		case porConta:
			t.Run(chave, func(t *testing.T) {
				// A conta de escopo total está fora do alcance de quem só
				// tem a Retaguarda: alterá-la seria mexer em quem enxerga
				// mais que ele.
				resp := c.s.como(c.retAdmin).do(t, metodo, c.concreto(padrao, 0, 0), map[string]any{"role": "viewer"})
				assertStatus(t, resp, http.StatusNotFound)
			})
		}
	}
}

// TestScopedAccountReachesItsOwnScope é o controle positivo: sem ele, uma
// rota que devolvesse 404 para tudo passaria no teste anterior.
func TestScopedAccountReachesItsOwnScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	for chave, cl := range rotas {
		metodo, padrao := separar(chave)
		if metodo != http.MethodGet || (cl != porMonitor && cl != porGrupo && cl != porCanal) {
			continue
		}
		t.Run(chave, func(t *testing.T) {
			caminho := c.concreto(padrao, c.dentro, c.retProd)
			if cl == porCanal {
				caminho = strings.ReplaceAll(padrao, "{id}", itoa(c.canalDentro))
			}
			resp := c.s.como(c.retViewer).do(t, metodo, caminho, nil)
			assertStatus(t, resp, http.StatusOK)
		})
	}
}

// vazou procura no corpo qualquer referência ao que está fora do escopo.
func (c *cenarioEscopo) vazou(t *testing.T, corpo string) string {
	t.Helper()
	for _, s := range segredos {
		if strings.Contains(corpo, s) {
			return s
		}
	}

	var v any
	if json.Unmarshal([]byte(corpo), &v) != nil {
		return ""
	}
	fora := map[float64]bool{float64(c.foraAgrupado): true, float64(c.foraSolto): true}
	var achado string
	var andar func(chave string, v any)
	andar = func(chave string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, filho := range x {
				andar(k, filho)
			}
		case []any:
			for _, filho := range x {
				if n, ok := filho.(float64); ok && chave == "components" && fora[n] {
					achado = "components contém monitor de fora"
				}
				andar(chave, filho)
			}
		case float64:
			if chave == "monitor_id" && fora[x] {
				achado = "monitor_id de fora"
			}
		}
	}
	andar("", v)
	return achado
}

// TestScopedListingsDoNotLeak lê cada listagem com a conta restrita.
func TestScopedListingsDoNotLeak(t *testing.T) {
	c := novoCenarioEscopo(t)

	var caminhos []string
	for chave, cl := range rotas {
		metodo, padrao := separar(chave)
		if cl == listagem && metodo == http.MethodGet {
			caminhos = append(caminhos, c.concreto(padrao, 0, 0))
		}
	}
	// Os filtros por parâmetro também: pedir explicitamente o que está
	// fora não pode furar o escopo.
	caminhos = append(caminhos,
		"/api/v1/incidents?monitor_id="+itoa(c.foraAgrupado),
		"/api/v1/monitors?group_id="+itoa(c.rpaProd),
		"/api/v1/monitors?group_id="+itoa(c.rpa),
	)

	for _, caminho := range caminhos {
		for conta, cookie := range map[string]*http.Cookie{"observador": c.retViewer, "admin": c.retAdmin} {
			t.Run(caminho+" "+conta, func(t *testing.T) {
				resp := c.s.como(cookie).do(t, http.MethodGet, caminho, nil)
				if resp.StatusCode == http.StatusForbidden && conta == "observador" {
					return // /users é só de administrador
				}
				assertStatus(t, resp, http.StatusOK)
				if s := c.vazou(t, readBody(t, resp)); s != "" {
					t.Fatalf("a listagem vazou %q", s)
				}
			})
		}
	}
}

// TestScopedListingsStillShowTheScope garante que o filtro não apagou
// tudo: listagem vazia não vaza, mas também não serve a ninguém.
func TestScopedListingsStillShowTheScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	for _, caminho := range []string{"/api/v1/monitors", "/api/v1/summaries", "/api/v1/problems", "/api/v1/groups/summary", "/api/v1/channels"} {
		t.Run(caminho, func(t *testing.T) {
			resp := c.s.como(c.retViewer).do(t, http.MethodGet, caminho, nil)
			assertStatus(t, resp, http.StatusOK)
			corpo := readBody(t, resp)
			if !strings.Contains(corpo, `"monitor_id":`+itoa(c.dentro)) &&
				!strings.Contains(corpo, `"id":`+itoa(c.dentro)) &&
				!strings.Contains(corpo, `"aviso-retaguarda"`) &&
				!strings.Contains(corpo, `"group_id":`+itoa(c.retProd)) {
				t.Fatalf("a listagem perdeu o que está no escopo: %s", corpo)
			}
		})
	}
}

// TestScopedAdminCannotTouchGlobalResources: canal, página pública e
// relato afetam monitores de todos os grupos, então só quem alcança tudo
// os altera. A resposta é 403 tanto para monitor de dentro quanto para o
// de fora, e por isso não serve de oráculo.
func TestScopedAdminCannotTouchGlobalResources(t *testing.T) {
	c := novoCenarioEscopo(t)

	for chave, cl := range rotas {
		if cl != escopoTotal {
			continue
		}
		metodo, padrao := separar(chave)
		for nome, alvo := range map[string][2]int64{
			"dentro": {c.dentro, c.retProd}, "fora": {c.foraAgrupado, c.rpaProd},
		} {
			t.Run(chave+" "+nome, func(t *testing.T) {
				resp := c.s.como(c.retAdmin).do(t, metodo, c.concreto(padrao, alvo[0], alvo[1]), map[string]any{})
				assertStatus(t, resp, http.StatusForbidden)
			})
		}
	}
}

// ---------- eventos ao vivo ----------

// TestEventStreamRespectsScope abre o fluxo com a conta restrita e mexe
// em monitores de fora. Nada disso pode chegar; o sentinela, de dentro,
// prova que o fluxo estava vivo e entregando.
func TestEventStreamRespectsScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.s.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(c.retViewer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	linhas := make(chan string, 100)
	go func() {
		defer close(linhas)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			linhas <- sc.Text()
		}
	}()

	// Dá tempo de a assinatura se registrar antes de publicar.
	time.Sleep(100 * time.Millisecond)

	admin := c.s.como(c.admin)
	admin.createMonitor(t, monitorNoGrupo("segredo-novo", &c.rpaProd))
	assertStatus(t, admin.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(c.foraAgrupado),
		monitorNoGrupo("segredo-rpa", &c.rpaProd)), http.StatusOK)
	assertStatus(t, admin.do(t, http.MethodDelete, "/api/v1/monitors/"+itoa(c.foraSolto), nil),
		http.StatusNoContent)
	// Mover o monitor de dentro para fora: quem o via recebe a saída
	// dele, mas não o destino.
	assertStatus(t, admin.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(c.dentro),
		monitorNoGrupo("bff", &c.rpaProd)), http.StatusOK)
	admin.createMonitor(t, monitorNoGrupo("sentinela", &c.retProd))

	var recebido []string
	for linha := range linhas {
		recebido = append(recebido, linha)
		if strings.Contains(linha, "sentinela") {
			break
		}
	}
	tudo := strings.Join(recebido, "\n")

	if !strings.Contains(tudo, "sentinela") {
		t.Fatalf("o fluxo não entregou nem o que está no escopo:\n%s", tudo)
	}
	for _, s := range append(segredos, "segredo-novo") {
		if strings.Contains(tudo, s) {
			t.Fatalf("o fluxo vazou %q:\n%s", s, tudo)
		}
	}
	if !strings.Contains(tudo, "event: monitor.deleted") ||
		!strings.Contains(tudo, `"id":`+itoa(c.dentro)) {
		t.Errorf("o monitor que saiu do escopo não foi retirado da tela:\n%s", tudo)
	}
	if strings.Contains(tudo, `"group_id":`+itoa(c.rpaProd)) {
		t.Errorf("o fluxo revelou o destino do monitor movido:\n%s", tudo)
	}
}
