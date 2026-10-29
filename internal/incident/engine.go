package incident

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/metrics"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
)

// Sink recebe a batida depois de o motor observá-la.
//
// Interface estreita: o motor não precisa saber gravar, só encaminhar.
type Sink interface {
	Submit(ctx context.Context, hb domain.Heartbeat) error
}

// Dispatcher entrega os avisos.
type Dispatcher interface {
	Enqueue(n notifier.Notification, channels []notifier.Notifier)
}

// Engine transforma batidas em incidentes e avisos.
//
// Fica entre o agendador e o escritor de lote: observa cada batida a
// caminho do banco, sem que nenhum dos dois precise saber que ele existe.
type Engine struct {
	next     Sink
	store    store.MetadataStore
	dispatch Dispatcher
	counters *metrics.Counters

	mu       sync.RWMutex
	monitors map[int64]domain.Monitor
	states   map[int64]domain.MonitorState

	// aliveAt é o instante da última observação nova com resposta de cada
	// monitor, e failingSince o começo da sequência de quedas observada.
	// É o que permite perguntar "o pai deu sinal de vida depois de o filho
	// começar a falhar?" (ver screen). Ficam só em memória: depois de um
	// reinício a pergunta fica sem resposta até o pai falar de novo, e a
	// retenção tem prazo para isso.
	aliveAt      map[int64]time.Time
	failingSince map[int64]time.Time

	// maintenance responde se o monitor está numa janela de manutenção.
	// Nulo é nenhuma janela (ver WithMaintenance).
	maintenance Maintenance
}

// NewEngine cria o motor.
//
// counters é opcional: quem não expõe métricas passa nil, e o contador
// nulo aceita observações sem fazer nada.
func NewEngine(next Sink, s store.MetadataStore, d Dispatcher, counters *metrics.Counters) *Engine {
	return &Engine{
		next:     next,
		store:    s,
		dispatch: d,
		counters: counters,
		monitors: map[int64]domain.Monitor{},
		states:   map[int64]domain.MonitorState{},

		aliveAt:      map[int64]time.Time{},
		failingSince: map[int64]time.Time{},
	}
}

// Load traz do banco o estado confirmado de cada monitor.
//
// Sem isto um reinício zeraria a contagem, e um alvo prestes a ser
// declarado fora do ar voltaria à estaca zero — atrasando a detecção em
// várias janelas justamente depois de uma manutenção.
func (e *Engine) Load(ctx context.Context) error {
	estados, err := e.store.States().All(ctx)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.states = estados
	e.mu.Unlock()
	return nil
}

// Upsert registra ou atualiza um monitor conhecido.
//
// O motor precisa do limiar e do nome a cada batida; consultá-los no
// banco toda vez seria uma leitura por verificação.
func (e *Engine) Upsert(m domain.Monitor) {
	e.mu.Lock()
	e.monitors[m.ID] = m
	e.mu.Unlock()
}

// Remove esquece um monitor apagado.
func (e *Engine) Remove(id int64) {
	e.mu.Lock()
	delete(e.monitors, id)
	delete(e.states, id)
	delete(e.aliveAt, id)
	delete(e.failingSince, id)
	e.mu.Unlock()
}

// Submit observa a batida e a encaminha.
//
// O encaminhamento acontece primeiro: gravar a medição é o caminho
// crítico, e uma falha ao decidir sobre alerta não pode custar o dado.
func (e *Engine) Submit(ctx context.Context, hb domain.Heartbeat) error {
	// A triagem vem antes da gravação porque decide o que é gravado: a
	// falha que não é do monitor vai para o histórico como lacuna. É só
	// memória, sem caminho de erro que custe o dado.
	hb = e.screen(hb)

	err := e.next.Submit(ctx, hb)

	// O motor vê toda batida a caminho do banco, então é aqui que a
	// contagem acumulada sai de graça — sem varrer a tabela a cada
	// raspagem de métricas.
	e.counters.ObserveCheck(hb.Status)

	// A avaliação nunca devolve erro para cima: o agendador não tem o que
	// fazer com ele, e propagá-lo só faria uma falha de notificação
	// parecer falha de monitoramento.
	e.evaluate(ctx, hb)

	return err
}

// evaluate roda a máquina e reage ao que ela decidir.
func (e *Engine) evaluate(ctx context.Context, hb domain.Heartbeat) {
	e.mu.Lock()
	monitor, conhecido := e.monitors[hb.MonitorID]
	anterior := e.states[hb.MonitorID]
	e.mu.Unlock()

	if !conhecido {
		// Batida de monitor que o motor não conhece: sem o limiar não há
		// como decidir, e inventar um padrão alertaria errado.
		return
	}

	// A confirmação conta observações, não leituras. A batida que repete
	// uma observação já contada — o mesmo sinal de push relido até chegar
	// o próximo — não move a máquina nem zera a contagem, como a amostra
	// sem medição: contá-la faria um único "down" abrir incidente sozinho
	// depois de N verificações.
	if hb.Repeated {
		return
	}

	proximo, eventos := Next(anterior, hb.Status, hb.Timestamp,
		Config{Threshold: monitor.ConfirmationThreshold})

	if proximo == anterior {
		return
	}

	e.mu.Lock()
	e.states[hb.MonitorID] = proximo
	e.mu.Unlock()

	if err := e.store.States().Save(ctx, hb.MonitorID, proximo); err != nil {
		slog.Error("falha ao gravar estado do monitor", "monitor", monitor.Name, "erro", err)
	}

	for _, evento := range eventos {
		e.record(ctx, monitor, evento, hb.Message)
		e.notify(ctx, monitor, evento, hb.Message)
	}
}

// screen decide se a falha do monitor é dele mesmo.
//
// Com um ancestral em queda confirmada, não é: a falha vira "sem
// medição", com o nome do ancestral e a causa original na mensagem. O
// motor não conta observação sem medição, então o filho não avisa nem
// abre incidente; e a disponibilidade a ignora, então o intervalo fica
// como lacuna, e não como queda de um serviço que pode ter continuado de
// pé. É a mesma saída da sentinela de rede, pelo mesmo motivo.
//
// Antes disso há uma corrida. Pai e filhos param juntos — a VM da sonda
// perdeu a saída —, mas cada um percebe na própria janela, e o filho
// costuma perceber primeiro. Por isso a falha do filho também fica retida
// enquanto o pai não decidiu: enquanto houver queda dele por confirmar,
// ou enquanto ele não tiver dado sinal de vida desde que o filho começou
// a falhar. A retenção tem prazo (holdLimit), para um pai que não diz
// nada não calar o filho para sempre; e não vale para pai sem medição,
// que nunca decidiria.
//
// A janela de manutenção vem antes de tudo isso (ver inMaintenance).
func (e *Engine) screen(hb domain.Heartbeat) domain.Heartbeat {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.track(hb)
	if w, ok := e.inMaintenance(hb); ok {
		return discard(hb, fmt.Sprintf("em manutenção — %s; resultado descartado (falha original: %s)",
			w.Title, originalCause(hb)))
	}
	if hb.Status != domain.StatusDown {
		return hb
	}
	m, ok := e.monitors[hb.MonitorID]
	if !ok || m.ParentID == nil {
		return hb
	}

	if pai, ok := domain.DownAncestor(m, e.lookup, e.stateOf); ok {
		return discard(hb, fmt.Sprintf("sem sinal — %s fora do ar; resultado descartado (falha original: %s)",
			pai.Name, hb.Message))
	}
	if pai, ok := e.undecidedParent(m, hb.Timestamp); ok {
		return discard(hb, fmt.Sprintf("aguardando %s mostrar que está no ar; resultado retido (falha original: %s)",
			pai.Name, hb.Message))
	}
	return hb
}

// track lembra o último sinal de vida e o começo da sequência de falhas.
//
// A batida repetida não é sinal de vida: é o mesmo sinal de push relido,
// e um pai que parou de reportar continuaria "vivo" até a janela fechar.
// Sem medição não muda nada, como na máquina de confirmação.
func (e *Engine) track(hb domain.Heartbeat) {
	switch {
	case hb.Status.Responsive():
		delete(e.failingSince, hb.MonitorID)
		if !hb.Repeated {
			e.aliveAt[hb.MonitorID] = hb.Timestamp
		}
	case hb.Status == domain.StatusDown:
		if _, ok := e.failingSince[hb.MonitorID]; !ok {
			e.failingSince[hb.MonitorID] = hb.Timestamp
		}
	}
}

// undecidedParent devolve o pai que ainda pode estar caindo junto.
func (e *Engine) undecidedParent(m domain.Monitor, at time.Time) (domain.Monitor, bool) {
	pai, ok := e.nearestEnabledParent(m)
	if !ok {
		return domain.Monitor{}, false
	}
	estado := e.states[pai.ID]
	if !estado.Status.Responsive() {
		return domain.Monitor{}, false
	}
	if estado.Candidate == domain.StatusDown && estado.Consecutive > 0 {
		return pai, true
	}

	desde := e.failingSince[m.ID]
	if vivo, ok := e.aliveAt[pai.ID]; ok && !vivo.Before(desde) {
		return domain.Monitor{}, false
	}
	return pai, at.Sub(desde) < holdLimit(pai)
}

// holdLimit é quanto a falha do filho espera o pai: o tempo que o pai
// leva para perceber a própria queda, mais uma verificação dele.
func holdLimit(pai domain.Monitor) time.Duration {
	return checker.DetectionWindow(pai) + pai.Interval
}

// nearestEnabledParent sobe até o primeiro ancestral ativo. O pausado não
// verifica nada, então não teria como decidir.
func (e *Engine) nearestEnabledParent(m domain.Monitor) (domain.Monitor, bool) {
	vistos := map[int64]bool{m.ID: true}
	for p := m.ParentID; p != nil && !vistos[*p]; {
		vistos[*p] = true
		pai, ok := e.monitors[*p]
		if !ok {
			return domain.Monitor{}, false
		}
		if pai.Enabled {
			return pai, true
		}
		p = pai.ParentID
	}
	return domain.Monitor{}, false
}

// discard troca a falha por "sem medição", com o motivo na mensagem.
func discard(hb domain.Heartbeat, msg string) domain.Heartbeat {
	hb.Status = domain.StatusUnknown
	hb.Message = msg
	return hb
}

// lookup e stateOf servem a domain.DownAncestor. Quem chama já segura mu.
func (e *Engine) lookup(id int64) (domain.Monitor, bool) {
	m, ok := e.monitors[id]
	return m, ok
}

func (e *Engine) stateOf(id int64) domain.MonitorState { return e.states[id] }

// dependents conta os monitores ativos calados por esta queda: os que
// têm o monitor como ancestral em queda mais próximo.
func (e *Engine) dependents(id int64) int {
	e.mu.RLock()
	defer e.mu.RUnlock()

	n := 0
	for _, m := range e.monitors {
		if !m.Enabled || m.ID == id {
			continue
		}
		if pai, ok := domain.DownAncestor(m, e.lookup, e.stateOf); ok && pai.ID == id {
			n++
		}
	}
	return n
}

// record mantém o histórico de incidentes e de mudanças de estado em dia.
func (e *Engine) record(ctx context.Context, m domain.Monitor, evt Event, cause string) {
	mudanca := domain.Transition{MonitorID: m.ID, From: evt.From, To: evt.To, At: evt.At, Cause: cause}
	if err := e.store.Transitions().Record(ctx, &mudanca); err != nil {
		slog.Error("falha ao registrar mudança de estado", "monitor", m.Name, "erro", err)
	}

	if evt.Kind == KindDown {
		incidente := domain.Incident{MonitorID: m.ID, StartedAt: evt.At, Cause: cause}

		if err := e.store.Incidents().Open(ctx, &incidente); err != nil {
			// Conflito significa que já havia uma queda aberta — o estado
			// em memória e o banco divergiram, o que não impede o alerta.
			if !errors.Is(err, store.ErrConflict) {
				slog.Error("falha ao abrir incidente", "monitor", m.Name, "erro", err)
			}
		}
		return
	}

	// Qualquer saída do estado de queda encerra o incidente, inclusive
	// passar para degradado: o alvo voltou a responder.
	if evt.Resolves() {
		if err := e.store.Incidents().Resolve(ctx, m.ID, evt.At); err != nil {
			slog.Error("falha ao encerrar incidente", "monitor", m.Name, "erro", err)
		}
	}
}

// notify monta os canais do monitor e entrega o aviso.
func (e *Engine) notify(ctx context.Context, m domain.Monitor, evt Event, cause string) {
	if e.dispatch == nil {
		return
	}

	// Destinations, e não ForMonitor: o canal vinculado ao grupo ou ao
	// sistema do monitor também é destino, e a regra de quem recebe mora
	// no store, num lugar só.
	canais, err := e.store.Channels().Destinations(ctx, m.ID)
	if err != nil {
		slog.Error("falha ao listar canais do monitor", "monitor", m.Name, "erro", err)
		return
	}
	if len(canais) == 0 {
		return
	}

	destinos := make([]notifier.Notifier, 0, len(canais))
	for _, canal := range canais {
		n, err := notifier.Build(canal.Type, canal.Config)
		if err != nil {
			// Canal mal configurado não pode calar os outros: o que dá
			// para entregar, se entrega.
			slog.Error("canal de aviso inválido",
				"canal", canal.Name, "tipo", canal.Type, "erro", err)
			continue
		}
		destinos = append(destinos, n)
	}

	aviso := notifier.Notification{
		Monitor: m,
		Event:   evt,
		Message: cause,
		Group:   e.groupPath(ctx, m),
	}
	if evt.Kind == KindDown {
		// Um aviso só no lugar de vários: quem recebe precisa saber que
		// ele vale pelos dependentes que ficaram calados.
		aviso.Dependents = e.dependents(m.ID)
	}
	e.dispatch.Enqueue(aviso, destinos)
}

// StateOf devolve o estado confirmado em memória, para inspeção.
func (e *Engine) StateOf(monitorID int64) domain.MonitorState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.states[monitorID]
}
