package incident

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/notifier"
	"github.com/bernardojoao/upwatch/internal/store"
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

	mu       sync.RWMutex
	monitors map[int64]domain.Monitor
	states   map[int64]domain.MonitorState
}

// NewEngine cria o motor.
func NewEngine(next Sink, s store.MetadataStore, d Dispatcher) *Engine {
	return &Engine{
		next:     next,
		store:    s,
		dispatch: d,
		monitors: map[int64]domain.Monitor{},
		states:   map[int64]domain.MonitorState{},
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
	e.mu.Unlock()
}

// Submit observa a batida e a encaminha.
//
// O encaminhamento acontece primeiro: gravar a medição é o caminho
// crítico, e uma falha ao decidir sobre alerta não pode custar o dado.
func (e *Engine) Submit(ctx context.Context, hb domain.Heartbeat) error {
	err := e.next.Submit(ctx, hb)

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

// record mantém o histórico de incidentes em dia.
func (e *Engine) record(ctx context.Context, m domain.Monitor, evt Event, cause string) {
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

	canais, err := e.store.Channels().ForMonitor(ctx, m.ID)
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

	e.dispatch.Enqueue(notifier.Notification{
		Monitor: m,
		Event:   evt,
		Message: cause,
	}, destinos)
}

// StateOf devolve o estado confirmado em memória, para inspeção.
func (e *Engine) StateOf(monitorID int64) domain.MonitorState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.states[monitorID]
}
