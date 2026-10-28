package checker

import (
	"encoding/json"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

func httpMonitor(t *testing.T, id int64, as ...JSONAssertion) domain.Monitor {
	t.Helper()
	cfg, err := json.Marshal(HTTPConfig{JSONAssertions: as})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Monitor{ID: id, Type: domain.MonitorHTTP, Config: cfg}
}

// Interpretar as asserções é trabalho do cadastro; o Check roda a cada
// poucos segundos por monitor e não deveria refazê-lo. O mesmo monitor com
// a mesma configuração reaproveita o que já foi interpretado — a mesma
// fatia, não uma cópia igual.
func TestHTTPCompilesAssertionsOncePerConfig(t *testing.T) {
	h := NewHTTP()
	m := httpMonitor(t, 7, JSONAssertion{Path: "$.db", Op: opExists})

	first, err := h.assertionsFor(m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.assertionsFor(m)
	if err != nil {
		t.Fatal(err)
	}

	if &first[0] != &second[0] {
		t.Error("assertionsFor compiled the same configuration twice")
	}
}

// Editar o monitor troca a configuração sem trocar o id. Servir a versão
// antiga do cache vigiaria o campo que a pessoa acabou de tirar.
func TestHTTPRecompilesAssertionsWhenConfigChanges(t *testing.T) {
	h := NewHTTP()
	if _, err := h.assertionsFor(httpMonitor(t, 7, JSONAssertion{Path: "$.db", Op: opExists})); err != nil {
		t.Fatal(err)
	}

	got, err := h.assertionsFor(httpMonitor(t, 7, JSONAssertion{Path: "$.cache", Op: opExists}))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Path != "$.cache" {
		t.Errorf("assertionsFor after an edit = %+v, want the new $.cache assertion", got)
	}
}
