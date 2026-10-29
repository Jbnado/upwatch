package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Monitores dependentes: o pai em queda cala os filhos.

type monitorComPai struct {
	ID       int64  `json:"id"`
	ParentID *int64 `json:"parent_id"`
}

func (s *server) paiDe(t *testing.T, id int64) *int64 {
	t.Helper()
	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusOK)
	return decode[monitorComPai](t, resp).ParentID
}

// A → B → A faria cada um calar o outro: os dois cairiam juntos sem que
// nenhum avisasse. Recusar só a autorreferência deixava esse passar.
func TestMonitorParentCycleIsRefused(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	a := s.createMonitor(t, newMonitorBody("sonda"))
	corpo := newMonitorBody("redis")
	corpo["parent_id"] = a
	b := s.createMonitor(t, corpo)

	corpo = newMonitorBody("sonda")
	corpo["parent_id"] = b
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(a), corpo)
	assertStatus(t, resp, http.StatusBadRequest)
	if !strings.Contains(readBody(t, resp), `"field":"parent_id"`) {
		t.Error("the error does not point at parent_id")
	}
}

// Ligar as verificações de uma sonda ao sinal de vida dela é o caso
// comum, e um pedido só serve: um a um, pela edição completa, cada
// monitor teria a configuração inteira reenviada.
func TestSetChildrenLinksManyAtOnce(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	pai := s.createMonitor(t, newMonitorBody("sonda"))
	a := s.createMonitor(t, newMonitorBody("redis"))
	b := s.createMonitor(t, newMonitorBody("postgres"))

	resp := s.do(t, http.MethodPost, "/api/v1/monitors/"+itoa(pai)+"/children",
		map[string]any{"monitor_ids": []int64{a, b}})
	assertStatus(t, resp, http.StatusOK)

	for _, id := range []int64{a, b} {
		if got := s.paiDe(t, id); got == nil || *got != pai {
			t.Errorf("monitor %d parent = %v, want %d", id, got, pai)
		}
	}
}

// O ciclo é recusado no lote também, e o lote inteiro: gravar metade
// deixaria a sonda com parte das verificações ligadas.
func TestSetChildrenRefusesACycle(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	avo := s.createMonitor(t, newMonitorBody("host"))
	corpo := newMonitorBody("sonda")
	corpo["parent_id"] = avo
	pai := s.createMonitor(t, corpo)
	solto := s.createMonitor(t, newMonitorBody("redis"))

	for nome, ids := range map[string][]int64{
		"o próprio pai": {solto, pai},
		"o avô":         {solto, avo},
	} {
		t.Run(nome, func(t *testing.T) {
			resp := s.do(t, http.MethodPost, "/api/v1/monitors/"+itoa(pai)+"/children",
				map[string]any{"monitor_ids": ids})
			assertStatus(t, resp, http.StatusBadRequest)
			if got := s.paiDe(t, solto); got != nil {
				t.Errorf("redis got parent %d from a refused batch", *got)
			}
		})
	}
}

// Filho de fora do escopo responde como o inexistente, e o lote não
// grava nada: aceitar ensinaria a quem chuta ids quais existem, e
// penduraria no pai um monitor que a conta nem enxerga.
func TestSetChildrenOutsideTheScopeIsNotFound(t *testing.T) {
	c := novoCenarioEscopo(t)
	adm := c.s.como(c.retAdmin)
	irmao := c.s.createMonitor(t, monitorNoGrupo("worker", &c.retProd))

	for nome, fora := range map[string]int64{"agrupado": c.foraAgrupado, "solto": c.foraSolto, "inexistente": 99999} {
		t.Run(nome, func(t *testing.T) {
			resp := adm.do(t, http.MethodPost, "/api/v1/monitors/"+itoa(c.dentro)+"/children",
				map[string]any{"monitor_ids": []int64{irmao, fora}})
			assertStatus(t, resp, http.StatusNotFound)
			if got := c.s.paiDe(t, irmao); got != nil {
				t.Errorf("worker got parent %d from a refused batch", *got)
			}
		})
	}

	// Controle positivo: o irmão de dentro do escopo serve.
	resp := adm.do(t, http.MethodPost, "/api/v1/monitors/"+itoa(c.dentro)+"/children",
		map[string]any{"monitor_ids": []int64{irmao}})
	assertStatus(t, resp, http.StatusOK)
}

// Desligar pela página do pai solta só o que depende dele: um id de
// filho de outra sonda responde como o inexistente e não muda nada.
func TestUnlinkChildOnlyFromItsOwnParent(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	sonda := s.createMonitor(t, newMonitorBody("sonda"))
	outra := s.createMonitor(t, newMonitorBody("outra sonda"))
	corpo := newMonitorBody("redis")
	corpo["parent_id"] = sonda
	redis := s.createMonitor(t, corpo)

	resp := s.do(t, http.MethodDelete, "/api/v1/monitors/"+itoa(outra)+"/children/"+itoa(redis), nil)
	assertStatus(t, resp, http.StatusNotFound)
	if got := s.paiDe(t, redis); got == nil || *got != sonda {
		t.Fatalf("redis parent = %v after a refused unlink, want %d", got, sonda)
	}

	resp = s.do(t, http.MethodDelete, "/api/v1/monitors/"+itoa(sonda)+"/children/"+itoa(redis), nil)
	assertStatus(t, resp, http.StatusNoContent)
	if got := s.paiDe(t, redis); got != nil {
		t.Errorf("redis parent = %d after unlink, want none", *got)
	}
}

type resumoAfetado struct {
	MonitorID  int64  `json:"monitor_id"`
	Status     string `json:"status"`
	AffectedBy *struct {
		ID   *int64 `json:"id"`
		Name string `json:"name"`
	} `json:"affected_by"`
}

// O filho calado pelo pai aparece com estado próprio — "sem sinal, a
// sonda está fora" —, e não como problema dele: em "Problemas agora" só
// o pai aparece, dizendo quantos dependentes ficaram sem sinal, e o
// cartão do grupo não conta o filho como problema.
func TestScreensShowChildrenAffectedByTheParent(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	sondas := s.criarGrupo(t, "Sondas", nil)
	vm := s.criarGrupo(t, "vm-01", &sondas)
	pai := s.createMonitor(t, monitorNoGrupo("sonda vm-01", &vm))
	corpo := monitorNoGrupo("redis", &vm)
	corpo["parent_id"] = pai
	filho := s.createMonitor(t, corpo)

	s.estado(t, pai, domain.StatusDown, epoch)
	s.estado(t, filho, domain.StatusUp, epoch)

	resp := s.do(t, http.MethodGet, "/api/v1/summaries", nil)
	assertStatus(t, resp, http.StatusOK)
	resumos := decode[struct {
		Items []resumoAfetado `json:"items"`
	}](t, resp).Items
	for _, r := range resumos {
		switch r.MonitorID {
		case filho:
			if r.AffectedBy == nil || r.AffectedBy.ID == nil || *r.AffectedBy.ID != pai || r.AffectedBy.Name != "sonda vm-01" {
				t.Errorf("child summary affected_by = %+v, want the parent", r.AffectedBy)
			}
		case pai:
			if r.AffectedBy != nil {
				t.Errorf("parent summary affected_by = %+v, want none", r.AffectedBy)
			}
		}
	}

	resp = s.do(t, http.MethodGet, "/api/v1/problems", nil)
	assertStatus(t, resp, http.StatusOK)
	problemas := decode[struct {
		Items []struct {
			MonitorID  int64 `json:"monitor_id"`
			Dependents int   `json:"dependents"`
		} `json:"items"`
	}](t, resp).Items
	if len(problemas) != 1 || problemas[0].MonitorID != pai || problemas[0].Dependents != 1 {
		t.Errorf("problems = %+v, want only the parent, with 1 dependent", problemas)
	}

	// O filho continua no ar pelo estado dele, mas ninguém o mede agora:
	// tirá-lo da conta dos problemas não pode pô-lo na dos que estão no ar.
	s.estado(t, filho, domain.StatusDown, epoch)
	resp = s.do(t, http.MethodGet, "/api/v1/groups/summary", nil)
	assertStatus(t, resp, http.StatusOK)
	cartoes := decode[struct {
		Items []struct {
			Path     string `json:"path"`
			Down     int    `json:"down"`
			Up       int    `json:"up"`
			Affected int    `json:"affected"`
			Problems int    `json:"problems"`
		} `json:"items"`
	}](t, resp).Items
	for _, g := range cartoes {
		if g.Path == "Sondas / vm-01" && (g.Down != 1 || g.Up != 0 || g.Affected != 1 || g.Problems != 1) {
			t.Errorf("group card = %+v, want the parent down and the child affected, not a problem", g)
		}
	}
}

// O pai de fora do escopo cala o filho de dentro, e o filho mostra que
// está calado — mas sem o nome nem o id do pai, que a conta não enxerga.
func TestAffectedByDoesNotLeakAHiddenParent(t *testing.T) {
	c := novoCenarioEscopo(t)
	ctx := context.Background()
	if err := c.s.store.Monitors().SetParent(ctx, &c.foraAgrupado, []int64{c.dentro}); err != nil {
		t.Fatal(err)
	}

	resp := c.s.como(c.retViewer).do(t, http.MethodGet, "/api/v1/summaries", nil)
	assertStatus(t, resp, http.StatusOK)
	corpo := readBody(t, resp)
	if strings.Contains(corpo, "segredo-rpa") || strings.Contains(corpo, `"id":`+itoa(c.foraAgrupado)) {
		t.Errorf("summaries leak the hidden parent: %s", corpo)
	}
	if !strings.Contains(corpo, `"affected_by":{"id":null}`) {
		t.Errorf("summaries hide that the child is silenced: %s", corpo)
	}
}
