package api

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// Exposição em formato Prometheus.
//
// É o pedido mais recorrente nas issues dos concorrentes, e a razão é
// prática: quem já opera com Prometheus não quer um segundo painel para
// olhar durante um incidente — quer a série no lugar onde o alerta já
// mora, ao lado das métricas do serviço monitorado.
//
// Escrito à mão, sem a biblioteca cliente oficial. O formato de texto é
// estável há uma década e cabe em cem linhas; a dependência traria um
// registrador global, coletores de runtime que ninguém pediu e alguns
// megabytes no binário que precisa continuar sendo um arquivo só.

// metricsMaxMonitors limita quantas séries por monitor são publicadas.
//
// Cardinalidade é como se derruba um Prometheus, e uma instalação com
// milhares de alvos publicaria milhares de séries a cada raspagem. O
// corte é anunciado numa métrica própria, não escondido.
const metricsMaxMonitors = 2000

// handleMetrics publica o estado atual em texto.
//
// Sem credencial: o Prometheus raspa sem sessão, e exigir uma obrigaria
// cada instalação a inventar um token só para isso. O que sai aqui são
// contagens e nomes de monitor — nunca endereço de alvo, que descreveria
// a topologia interna e ainda traria cardinalidade alta.
func (a *API) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var b strings.Builder

	family(&b, "upwatch_build_info", "gauge",
		"Versão em execução, sempre com valor 1.")
	fmt.Fprintf(&b, "upwatch_build_info{version=%s} 1\n", quote(a.version))

	monitores, err := a.store.Monitors().List(ctx, store.MonitorFilter{
		Page: store.PageFilter{Limit: store.MaxPageSize},
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	family(&b, "upwatch_monitors_total", "gauge",
		"Quantidade de monitores cadastrados.")
	fmt.Fprintf(&b, "upwatch_monitors_total %d\n", len(monitores.Items))

	estados, err := a.store.States().All(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	itens := monitores.Items
	truncados := 0
	if len(itens) > metricsMaxMonitors {
		truncados = len(itens) - metricsMaxMonitors
		itens = itens[:metricsMaxMonitors]
	}

	// Ordem estável: a exposição é lida por humano tanto quanto por
	// coletor, e ordem variável entre raspagens atrapalha comparar.
	sort.Slice(itens, func(i, j int) bool { return itens[i].Name < itens[j].Name })

	family(&b, "upwatch_monitor_status", "gauge",
		"Estado confirmado: 1 no ar, 0 fora do ar, 2 degradado, -1 sem medição.")
	for _, m := range itens {
		fmt.Fprintf(&b, "upwatch_monitor_status{monitor=%s,type=%s} %d\n",
			quote(m.Name), quote(m.Type.String()), statusValue(estados[m.ID].Status))
	}

	family(&b, "upwatch_monitor_enabled", "gauge",
		"1 quando o monitor está ativo, 0 quando pausado.")
	for _, m := range itens {
		fmt.Fprintf(&b, "upwatch_monitor_enabled{monitor=%s} %d\n", quote(m.Name), boolValue(m.Enabled))
	}

	family(&b, "upwatch_monitor_interval_seconds", "gauge",
		"Intervalo configurado entre verificações.")
	for _, m := range itens {
		fmt.Fprintf(&b, "upwatch_monitor_interval_seconds{monitor=%s} %g\n",
			quote(m.Name), m.Interval.Seconds())
	}

	// Uma consulta só para todos os monitores. Uma por alvo faria a
	// exposição custar N leituras a cada raspagem, e a métrica viraria a
	// maior fonte de carga do banco que ela observa.
	ultimas, err := a.store.LatestHeartbeats(ctx)
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Um laço por família, e não um laço emitindo as duas. As amostras de
	// uma família precisam sair agrupadas depois do seu HELP e TYPE;
	// intercalá-las faz o analisador estrito recusar a exposição inteira,
	// e o tolerante aceitar hoje e recusar amanhã.
	//
	// A latência sai em segundos, não em milissegundos: é a unidade base
	// da convenção do Prometheus, e misturar unidades entre exposições
	// quebra as funções de agregação de quem consome.
	family(&b, "upwatch_monitor_latency_seconds", "gauge",
		"Latência da última verificação, em segundos.")
	for _, m := range itens {
		ultima, ok := ultimas[m.ID]
		if !ok {
			// Monitor sem verificação não publica série de latência: um
			// zero ali entraria nas médias de quem consome como se fosse
			// resposta instantânea.
			continue
		}
		fmt.Fprintf(&b, "upwatch_monitor_latency_seconds{monitor=%s} %g\n",
			quote(m.Name), float64(ultima.LatencyMS)/1000)
	}

	family(&b, "upwatch_monitor_last_check_timestamp_seconds", "gauge",
		"Instante da última verificação, em segundos desde a época.")
	for _, m := range itens {
		ultima, ok := ultimas[m.ID]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "upwatch_monitor_last_check_timestamp_seconds{monitor=%s} %d\n",
			quote(m.Name), ultima.Timestamp.Unix())
	}

	abertos, err := a.store.Incidents().List(ctx, store.IncidentFilter{
		OnlyOpen: true,
		Page:     store.PageFilter{Limit: store.MaxPageSize},
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Zero é publicado, e não omitido: uma série que só aparece quando há
	// incidente deixa o gráfico vazio no período saudável, e o alerta não
	// distingue "nenhum" de "não coletei".
	family(&b, "upwatch_incidents_open", "gauge",
		"Quedas confirmadas ainda em curso.")
	fmt.Fprintf(&b, "upwatch_incidents_open %d\n", len(abertos.Items))

	// Contador acumulado no processo, não derivado da tabela de batidas:
	// varrer a tabela a cada raspagem faria a métrica pesar mais que o
	// monitoramento. Zerar no reinício é esperado — o Prometheus
	// reconhece a queda de um contador como reinício.
	family(&b, "upwatch_checks_total", "counter",
		"Verificações concluídas por estado desde o início do processo.")
	for _, s := range []domain.Status{
		domain.StatusUp, domain.StatusDegraded, domain.StatusDown, domain.StatusUnknown,
	} {
		fmt.Fprintf(&b, "upwatch_checks_total{status=%s} %d\n", quote(s.String()), a.counters.Checks(s))
	}

	family(&b, "upwatch_notifications_total", "counter",
		"Avisos entregues e descartados desde o início do processo.")
	fmt.Fprintf(&b, "upwatch_notifications_total{outcome=\"sent\"} %d\n", a.counters.NotificationsSent())
	fmt.Fprintf(&b, "upwatch_notifications_total{outcome=\"dropped\"} %d\n", a.counters.NotificationsDropped())

	family(&b, "upwatch_metrics_truncated_monitors", "gauge",
		"Monitores omitidos por limite de cardinalidade nesta exposição.")
	fmt.Fprintf(&b, "upwatch_metrics_truncated_monitors %d\n", truncados)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

// family escreve o cabeçalho de uma família de métricas.
func family(b *strings.Builder, nome, tipo, ajuda string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", nome, ajuda, nome, tipo)
}

// quote escapa um valor de rótulo.
//
// Um nome de monitor com aspas ou quebra de linha quebraria a exposição
// inteira: o Prometheus descarta a coleta e nenhuma métrica chega, nem as
// das séries saudáveis.
func quote(v string) string {
	var b strings.Builder
	b.Grow(len(v) + 2)
	b.WriteByte('"')

	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')
	return b.String()
}

// statusValue traduz o estado num número.
//
// "Sem medição" é -1, e não zero: zero significa "fora do ar", e
// confundir os dois faria o alerta disparar para todo monitor
// recém-criado.
func statusValue(s domain.Status) int {
	switch s {
	case domain.StatusUp:
		return 1
	case domain.StatusDown:
		return 0
	case domain.StatusDegraded:
		return 2
	default:
		return -1
	}
}

func boolValue(b bool) int {
	if b {
		return 1
	}
	return 0
}
