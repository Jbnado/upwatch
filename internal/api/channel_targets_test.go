package api_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Onde o canal está ligado, gravado junto com o canal.
//
// O formulário do canal escolhe grupos e monitores e salva tudo de uma
// vez. Gravar o canal e depois os vínculos um a um deixaria, na falha do
// meio, um canal que avisa sobre parte do que foi pedido — e quem salvou
// acharia que avisa sobre tudo.

type alvosResp struct {
	GroupIDs   []int64 `json:"group_ids"`
	MonitorIDs []int64 `json:"monitor_ids"`
}

type canalComAlvos struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Targets alvosResp `json:"targets"`
}

func webhookCanal(nome string, alvos map[string]any) map[string]any {
	corpo := map[string]any{
		"name": nome, "type": "webhook",
		"config": map[string]any{"url": "https://exemplo.com/hook"},
	}
	if alvos != nil {
		corpo["targets"] = alvos
	}
	return corpo
}

func (s *server) alvosNaLista(t *testing.T, canal int64) (alvosResp, bool) {
	t.Helper()
	resp := s.do(t, http.MethodGet, "/api/v1/channels", nil)
	assertStatus(t, resp, http.StatusOK)
	for _, c := range decode[struct{ Items []canalComAlvos }](t, resp).Items {
		if c.ID == canal {
			return c.Targets, true
		}
	}
	return alvosResp{}, false
}

func TestChannelCreateLinksTargetsInTheSameRequest(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)
	vazio := s.criarGrupo(t, "Vazio", nil)
	api := s.createMonitor(t, monitorNoGrupo("api", &prod))

	resp := s.do(t, http.MethodPost, "/api/v1/channels", webhookCanal("plantão", map[string]any{
		"group_ids": []int64{vazio, rpa, rpa}, "monitor_ids": []int64{api},
	}))
	assertStatus(t, resp, http.StatusCreated)
	criado := decode[canalComAlvos](t, resp)

	want := alvosResp{GroupIDs: []int64{rpa, vazio}, MonitorIDs: []int64{api}}
	if !reflect.DeepEqual(criado.Targets, want) {
		t.Errorf("POST targets = %+v, want %+v (em ordem, sem repetidos)", criado.Targets, want)
	}
	if got, _ := s.alvosNaLista(t, criado.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("list targets = %+v, want %+v", got, want)
	}

	resp = s.do(t, http.MethodGet, "/api/v1/channels/"+itoa(criado.ID), nil)
	assertStatus(t, resp, http.StatusOK)
	if got := decode[canalComAlvos](t, resp).Targets; !reflect.DeepEqual(got, want) {
		t.Errorf("GET targets = %+v, want %+v", got, want)
	}
}

func TestChannelWithoutTargetsListsEmptyArrays(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	id := s.criarCanal(t, "webhook", map[string]any{"url": "https://exemplo.com/hook"})

	resp := s.do(t, http.MethodGet, "/api/v1/channels", nil)
	assertStatus(t, resp, http.StatusOK)
	if corpo := readBody(t, resp); !strings.Contains(corpo, `"targets":{"group_ids":[],"monitor_ids":[]}`) {
		t.Errorf("list = %s, want empty targets arrays for channel %d", corpo, id)
	}
}

func TestChannelSetTargetsReplacesTheWholeSet(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	ret := s.criarGrupo(t, "Retaguarda", nil)
	api := s.createMonitor(t, monitorNoGrupo("api", &rpa))

	resp := s.do(t, http.MethodPost, "/api/v1/channels", webhookCanal("plantão", map[string]any{
		"group_ids": []int64{rpa}, "monitor_ids": []int64{api},
	}))
	assertStatus(t, resp, http.StatusCreated)
	canal := decode[canalComAlvos](t, resp).ID

	resp = s.do(t, http.MethodPut, "/api/v1/channels/"+itoa(canal)+"/targets", map[string]any{
		"group_ids": []int64{ret}, "monitor_ids": []int64{},
	})
	assertStatus(t, resp, http.StatusOK)
	want := alvosResp{GroupIDs: []int64{ret}, MonitorIDs: []int64{}}
	if got := decode[alvosResp](t, resp); !reflect.DeepEqual(got, want) {
		t.Errorf("PUT targets = %+v, want %+v", got, want)
	}
	if got, _ := s.alvosNaLista(t, canal); !reflect.DeepEqual(got, want) {
		t.Errorf("list targets = %+v, want %+v", got, want)
	}
}

func TestChannelUpdateKeepsTargetsUnlessSent(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	ret := s.criarGrupo(t, "Retaguarda", nil)
	resp := s.do(t, http.MethodPost, "/api/v1/channels", webhookCanal("plantão", map[string]any{
		"group_ids": []int64{rpa},
	}))
	assertStatus(t, resp, http.StatusCreated)
	canal := decode[canalComAlvos](t, resp).ID
	caminho := "/api/v1/channels/" + itoa(canal)

	assertStatus(t, s.do(t, http.MethodPut, caminho, webhookCanal("plantão 2", nil)), http.StatusOK)
	if got, _ := s.alvosNaLista(t, canal); !reflect.DeepEqual(got.GroupIDs, []int64{rpa}) {
		t.Errorf("targets after edit without targets = %+v, want RPA kept", got)
	}

	assertStatus(t, s.do(t, http.MethodPut, caminho, webhookCanal("plantão 3", map[string]any{
		"group_ids": []int64{ret},
	})), http.StatusOK)
	if got, _ := s.alvosNaLista(t, canal); !reflect.DeepEqual(got.GroupIDs, []int64{ret}) {
		t.Errorf("targets after edit with targets = %+v, want only Retaguarda", got)
	}
}

// Um alvo que não existe no meio do conjunto recusa tudo, com o id na
// mensagem para a tela dizer qual — e nada é gravado.
func TestChannelTargetsRefuseMissingWithoutWritingAnything(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)

	resp := s.do(t, http.MethodPost, "/api/v1/channels", webhookCanal("plantão", map[string]any{
		"group_ids": []int64{rpa}, "monitor_ids": []int64{9999},
	}))
	assertStatus(t, resp, http.StatusNotFound)
	if corpo := readBody(t, resp); !strings.Contains(corpo, "monitor 9999") {
		t.Errorf("error = %s, want it to name monitor 9999", corpo)
	}
	resp = s.do(t, http.MethodGet, "/api/v1/channels", nil)
	assertStatus(t, resp, http.StatusOK)
	if corpo := readBody(t, resp); strings.Contains(corpo, "plantão") {
		t.Errorf("list after refused create = %s, want no channel", corpo)
	}

	resp = s.do(t, http.MethodPost, "/api/v1/channels", webhookCanal("plantão", map[string]any{
		"group_ids": []int64{rpa},
	}))
	assertStatus(t, resp, http.StatusCreated)
	canal := decode[canalComAlvos](t, resp).ID

	for nome, corpo := range map[string]map[string]any{
		"grupo inexistente": {"group_ids": []int64{9999}},
		"monitor":           {"monitor_ids": []int64{9999}},
	} {
		t.Run(nome, func(t *testing.T) {
			resp := s.do(t, http.MethodPut, "/api/v1/channels/"+itoa(canal)+"/targets", corpo)
			assertStatus(t, resp, http.StatusNotFound)
			if got, _ := s.alvosNaLista(t, canal); !reflect.DeepEqual(got.GroupIDs, []int64{rpa}) {
				t.Errorf("targets after refused PUT = %+v, want RPA kept", got)
			}
		})
	}

	resp = s.do(t, http.MethodPut, "/api/v1/channels/"+itoa(canal), webhookCanal("outro nome", map[string]any{
		"group_ids": []int64{9999},
	}))
	assertStatus(t, resp, http.StatusNotFound)
	resp = s.do(t, http.MethodGet, "/api/v1/channels/"+itoa(canal), nil)
	assertStatus(t, resp, http.StatusOK)
	if got := decode[canalComAlvos](t, resp); got.Name != "plantão" {
		t.Errorf("name after refused edit = %q, want the old one", got.Name)
	}
}

func TestChannelTargetsRefuseInvalidIDs(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	canal := s.criarCanal(t, "webhook", map[string]any{"url": "https://exemplo.com/hook"})
	resp := s.do(t, http.MethodPut, "/api/v1/channels/"+itoa(canal)+"/targets", map[string]any{
		"monitor_ids": []int64{0},
	})
	assertStatus(t, resp, http.StatusBadRequest)

	resp = s.do(t, http.MethodPut, "/api/v1/channels/9999/targets", map[string]any{})
	assertStatus(t, resp, http.StatusNotFound)
}

// Quem tem escopo vê o canal que avisa sobre algo dele, mas a lista de
// onde o canal está ligado não pode contar o que está fora — nem como id.
func TestChannelTargetsListOnlyTheViewersScope(t *testing.T) {
	c := novoCenarioEscopo(t)

	for _, rota := range []string{
		"/api/v1/groups/" + itoa(c.rpaProd) + "/channels/" + itoa(c.canalDentro),
		"/api/v1/monitors/" + itoa(c.foraAgrupado) + "/channels/" + itoa(c.canalDentro),
		"/api/v1/monitors/" + itoa(c.dentro) + "/channels/" + itoa(c.canalDentro),
	} {
		assertStatus(t, c.s.do(t, http.MethodPut, rota, nil), http.StatusNoContent)
	}

	want := alvosResp{GroupIDs: []int64{c.retProd}, MonitorIDs: []int64{c.dentro}}
	got, ok := c.s.como(c.retViewer).alvosNaLista(t, c.canalDentro)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("scoped list targets = %+v (found %v), want %+v", got, ok, want)
	}

	resp := c.s.como(c.retViewer).do(t, http.MethodGet, "/api/v1/channels/"+itoa(c.canalDentro), nil)
	assertStatus(t, resp, http.StatusOK)
	if got := decode[canalComAlvos](t, resp).Targets; !reflect.DeepEqual(got, want) {
		t.Errorf("scoped GET targets = %+v, want %+v", got, want)
	}

	todos, _ := c.s.alvosNaLista(t, c.canalDentro)
	if len(todos.GroupIDs) != 2 || len(todos.MonitorIDs) != 2 {
		t.Errorf("full-scope list targets = %+v, want both groups and both monitors", todos)
	}
}
