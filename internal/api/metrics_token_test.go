package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
)

// /metrics com token.
//
// Sem credencial, a exposição publica o nome de todos os monitores, e
// numa instalação na internet isso descreve a topologia interna. O token
// é opcional para não obrigar quem raspa na rede local a configurar nada.

func (s *server) metricasCom(t *testing.T, cabecalho string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cabecalho != "" {
		req.Header.Set("Authorization", cabecalho)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestMetricsTokenIsRequiredWhenConfigured(t *testing.T) {
	s := newServerWith(t, func(o *api.Options) { o.MetricsToken = "segredo-do-prometheus" })
	s.setup(t)
	s.createMonitor(t, newMonitorBody("api-interna"))

	for nome, cabecalho := range map[string]string{
		"sem credencial": "",
		"token errado":   "Bearer outro",
		"prefixo apenas": "Bearer segredo",
		// A sessão da interface não serve: o token de métricas é outra
		// credencial, que pode ir para a configuração do Prometheus sem
		// dar acesso à API.
		"esquema errado": "Basic segredo-do-prometheus",
	} {
		t.Run(nome, func(t *testing.T) {
			resp := s.metricasCom(t, cabecalho)
			assertStatus(t, resp, http.StatusUnauthorized)
			if strings.Contains(readBody(t, resp), "api-interna") {
				t.Error("a recusa vazou o nome do monitor")
			}
		})
	}

	resp := s.metricasCom(t, "Bearer segredo-do-prometheus")
	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), `monitor="api-interna"`) {
		t.Error("com o token certo a exposição veio sem o monitor")
	}
}

// O nome do monitor só é único dentro do grupo. Sem o rótulo do grupo, o
// "bff" de produção e o de homologação viravam a mesma série, e o
// Prometheus recusaria a coleta inteira por série duplicada.
func TestMetricsLabelTheGroup(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	rpa := s.criarGrupo(t, "RPA", nil)
	prod := s.criarGrupo(t, "prod", &rpa)
	hml := s.criarGrupo(t, "hml", &rpa)
	s.createMonitor(t, monitorNoGrupo("bff", &prod))
	s.createMonitor(t, monitorNoGrupo("bff", &hml))

	corpo := s.metricas(t)
	for _, serie := range []string{
		`upwatch_monitor_enabled{monitor="bff",group="RPA / prod"} 1`,
		`upwatch_monitor_enabled{monitor="bff",group="RPA / hml"} 1`,
	} {
		if !strings.Contains(corpo, serie) {
			t.Errorf("faltou a série %s:\n%s", serie, corpo)
		}
	}
}
