package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// O endpoint que faz o UpWatch conversar com quem já tem Prometheus.
//
// É o pedido mais recorrente nas issues dos concorrentes, e a razão é
// prática: quem opera não quer um segundo painel para olhar durante um
// incidente — quer as métricas no lugar onde já mora o alerta.
//
// O que se verifica aqui é sobretudo o formato. Uma exposição malformada
// não falha alto: o Prometheus descarta a coleta em silêncio, e o
// operador só descobre quando procura o gráfico e não acha.

// metricas devolve o corpo do endpoint.
func (s *server) metricas(t *testing.T) string {
	t.Helper()

	resp := s.do(t, http.MethodGet, "/metrics", nil)
	assertStatus(t, resp, http.StatusOK)

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type inesperado: %q", ct)
	}
	return readBody(t, resp)
}

func TestMetricsDescribeEveryFamily(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	corpo := s.metricas(t)

	// HELP e TYPE não são enfeite: sem eles a métrica aparece sem
	// descrição em qualquer navegador de métricas, e quem não escreveu o
	// código não descobre o que ela mede.
	for _, familia := range []string{
		"upwatch_build_info",
		"upwatch_monitors_total",
		"upwatch_monitor_status",
		"upwatch_monitor_latency_seconds",
		"upwatch_incidents_open",
		"upwatch_checks_total",
	} {
		t.Run(familia, func(t *testing.T) {
			if !strings.Contains(corpo, "# HELP "+familia+" ") {
				t.Errorf("família %s sem HELP:\n%s", familia, corpo)
			}
			if !strings.Contains(corpo, "# TYPE "+familia+" ") {
				t.Errorf("família %s sem TYPE:\n%s", familia, corpo)
			}
		})
	}
}

func TestMetricsExposeMonitorState(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "api-de-producao", "type": "http", "target": "https://exemplo.com",
		"interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)

	corpo := s.metricas(t)

	// O nome do monitor vira rótulo: sem ele a série não diz de qual alvo
	// se trata, e um painel com vinte linhas iguais não serve para nada.
	if !strings.Contains(corpo, `monitor="api-de-producao"`) {
		t.Errorf("métrica sem o rótulo do monitor:\n%s", corpo)
	}
	if !strings.Contains(corpo, "upwatch_monitors_total 1") {
		t.Errorf("contagem de monitores divergiu:\n%s", corpo)
	}
}

func TestMetricsEscapeHostileLabel(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// Um nome com aspas ou quebra de linha quebraria a exposição inteira:
	// o Prometheus descarta a coleta e nenhuma métrica chega, nem as das
	// séries saudáveis.
	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": `alvo "com" \aspas` + "\n e quebra", "type": "http",
		"target": "https://exemplo.com", "interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)

	corpo := s.metricas(t)

	for _, linha := range strings.Split(corpo, "\n") {
		if !strings.HasPrefix(linha, "upwatch_monitor_status{") {
			continue
		}
		// Depois do escape, a linha precisa continuar sendo uma linha só,
		// com o rótulo fechando corretamente.
		if strings.Count(linha, `"`)%2 != 0 {
			t.Fatalf("aspas desbalanceadas na exposição: %q", linha)
		}
	}
	if strings.Contains(corpo, "\n e quebra") {
		t.Errorf("quebra de linha não escapada:\n%s", corpo)
	}
	if !strings.Contains(corpo, `\n`) {
		t.Errorf("a quebra deveria virar sequência de escape:\n%s", corpo)
	}
}

func TestMetricsCountIncidents(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	corpo := s.metricas(t)

	// Zero precisa ser publicado, e não omitido: uma série que só aparece
	// quando há incidente faz o gráfico ficar vazio no período saudável, e
	// o alerta não consegue distinguir "zero" de "não coletei".
	if !strings.Contains(corpo, "upwatch_incidents_open 0") {
		t.Errorf("contagem de incidentes ausente:\n%s", corpo)
	}
}

func TestMetricsNeedNoCredential(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.cookie = nil

	// O Prometheus raspa sem sessão, e exigir credencial aqui obrigaria
	// cada instalação a inventar um token só para isso. O endpoint só
	// expõe contagens e nomes de monitor — o mesmo que o painel mostra a
	// quem já entrou.
	resp := s.do(t, http.MethodGet, "/metrics", nil)

	assertStatus(t, resp, http.StatusOK)
}

func TestMetricsDoNotLeakTargets(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": "banco", "type": "tcp", "target": "banco-interno.vpc.local:5432",
		"interval_seconds": 60, "timeout_seconds": 10,
	})
	assertStatus(t, resp, http.StatusCreated)

	s.cookie = nil
	corpo := s.metricas(t)

	// O endereço do alvo não entra em rótulo. Além de descrever a
	// topologia interna, endereço em rótulo é cardinalidade alta — é como
	// se derruba um Prometheus.
	for _, agulha := range []string{"banco-interno.vpc.local", "5432"} {
		if strings.Contains(corpo, agulha) {
			t.Errorf("métricas revelaram %q:\n%s", agulha, corpo)
		}
	}
}

func TestMetricsKeepFamiliesContiguous(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	for _, nome := range []string{"api", "console", "webhooks"} {
		resp := s.do(t, http.MethodPost, "/api/v1/monitors", map[string]any{
			"name": nome, "type": "http", "target": "https://exemplo.com/" + nome,
			"interval_seconds": 60, "timeout_seconds": 10,
		})
		assertStatus(t, resp, http.StatusCreated)
	}

	corpo := s.metricas(t)

	// As amostras de uma família precisam sair juntas, depois do seu HELP
	// e TYPE. Intercalar duas famílias faz o analisador estrito recusar a
	// exposição inteira — e o tolerante aceitar hoje e recusar amanhã.
	vistas := map[string]bool{}
	atual := ""

	for _, linha := range strings.Split(corpo, "\n") {
		if linha == "" || strings.HasPrefix(linha, "# HELP ") {
			continue
		}
		if strings.HasPrefix(linha, "# TYPE ") {
			atual = strings.Fields(linha)[2]
			if vistas[atual] {
				t.Fatalf("família %q declarada duas vezes", atual)
			}
			vistas[atual] = true
			continue
		}

		familia := linha
		if i := strings.IndexAny(linha, "{ "); i > 0 {
			familia = linha[:i]
		}
		if familia != atual {
			t.Fatalf("amostra de %q apareceu no bloco de %q", familia, atual)
		}
	}
}
