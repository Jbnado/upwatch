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

// Sem token, /metrics publica nome de monitor e de grupo de toda a
// instalação, fora de qualquer sessão. Enquanto todas as contas veem tudo
// isso não muda nada; a partir da primeira conta restrita, a exposição
// aberta passa a ser o caminho para ela ver o que o escopo esconde. A
// resposta diz o que fazer, em vez de só recusar.
func TestMetricsWithoutTokenRefusesOnceAScopedAccountExists(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, newMonitorBody("api-interna"))

	assertStatus(t, s.metricasCom(t, ""), http.StatusOK)

	g := s.criarGrupo(t, "Retaguarda", nil)
	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "restrita", "password": "senha-de-teste-12345", "role": "viewer",
		"scope": map[string]any{"all_groups": false, "group_ids": []int64{g}},
	})
	assertStatus(t, resp, http.StatusCreated)

	resp = s.metricasCom(t, "")
	assertStatus(t, resp, http.StatusForbidden)
	corpo := readBody(t, resp)
	if !strings.Contains(corpo, "UPWATCH_METRICS_TOKEN") {
		t.Errorf("a recusa não orienta a definir o token: %s", corpo)
	}
	if strings.Contains(corpo, "api-interna") {
		t.Error("a recusa vazou o nome do monitor")
	}
}

// O aviso de início diz a mesma coisa no log, antes de alguém raspar e
// receber 403 sem entender.
func TestMetricsExposureWarning(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	ctx := context.Background()

	if msg, err := api.MetricsExposureWarning(ctx, s.store, ""); err != nil || msg != "" {
		t.Fatalf("sem conta restrita: aviso = %q, %v; want nenhum", msg, err)
	}

	g := s.criarGrupo(t, "Retaguarda", nil)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "restrita", "password": "senha-de-teste-12345", "role": "viewer",
		"scope": map[string]any{"all_groups": false, "group_ids": []int64{g}},
	}), http.StatusCreated)

	msg, err := api.MetricsExposureWarning(ctx, s.store, "")
	if err != nil || !strings.Contains(msg, "UPWATCH_METRICS_TOKEN") {
		t.Fatalf("com conta restrita e sem token: aviso = %q, %v", msg, err)
	}
	if msg, _ := api.MetricsExposureWarning(ctx, s.store, "um-token"); msg != "" {
		t.Errorf("com token definido não há o que avisar, veio %q", msg)
	}
}
