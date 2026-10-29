package incident

import (
	"context"
	"log/slog"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
)

// DefaultReminderTick é de quanto em quanto tempo as quedas abertas são
// revistas. O lembrete sai com até isso de atraso, o que não importa para
// um intervalo de, no mínimo, cinco minutos.
const DefaultReminderTick = time.Minute

// ReminderOptions configura os lembretes.
type ReminderOptions struct {
	Clock clock.Clock
	// Default é o intervalo de quem não escolheu nenhum, nem no grupo.
	// Zero desliga.
	Default time.Duration
	Tick    time.Duration

	// UnderMaintenance diz se o monitor está em janela de manutenção (#37).
	// Lembrete durante manutenção é ruído sobre uma queda que alguém
	// anunciou de antemão. Nulo enquanto as janelas não existirem.
	UnderMaintenance func(ctx context.Context, m domain.Monitor, at time.Time) bool
}

// Reminders relembra as quedas que seguem abertas sem que ninguém tenha
// dito estar olhando.
//
// O aviso de queda sai uma vez, na transição. A das duas da manhã se perde,
// e quem acorda às oito não vê nada novo no canal: parece que está tudo
// bem. O lembrete é o que muda isso, e o reconhecimento é o que impede
// que ele vire ruído para quem já está resolvendo.
//
// Laço à parte, e não um passo da avaliação das batidas: o lembrete
// depende do relógio, não de uma batida nova, e o monitor calado pelo pai
// nem gera batida que conte.
type Reminders struct {
	engine *Engine
	opts   ReminderOptions
}

// NewReminders cria o laço de lembretes sobre o estado do motor.
func NewReminders(e *Engine, opts ReminderOptions) *Reminders {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultReminderTick
	}
	return &Reminders{engine: e, opts: opts}
}

// Run revê as quedas abertas até o contexto terminar.
func (r *Reminders) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.opts.Clock.After(r.opts.Tick):
			r.Tick(ctx)
		}
	}
}

// Tick envia os lembretes vencidos.
func (r *Reminders) Tick(ctx context.Context) {
	e := r.engine
	if e.dispatch == nil {
		return
	}

	abertos, err := r.openIncidents(ctx)
	if err != nil {
		slog.Error("falha ao listar quedas abertas para lembrete", "erro", err)
		return
	}
	if len(abertos) == 0 {
		return
	}

	grupos, err := e.store.Groups().List(ctx)
	if err != nil {
		slog.Error("falha ao ler os grupos para lembrete", "erro", err)
		return
	}
	arvore := domain.NewGroupTree(grupos)
	agora := r.opts.Clock.Now()

	for _, inc := range abertos {
		if inc.Acknowledgement != nil {
			continue
		}
		m, ok := e.reminderTarget(inc.MonitorID)
		if !ok {
			continue
		}
		intervalo := domain.ReminderInterval(m, arvore, r.opts.Default)
		if intervalo <= 0 {
			continue
		}
		ultimo := inc.StartedAt
		if inc.RemindedAt != nil {
			ultimo = *inc.RemindedAt
		}
		if agora.Sub(ultimo) < intervalo {
			continue
		}
		// TODO(#37): ligar UnderMaintenance às janelas de manutenção.
		if r.opts.UnderMaintenance != nil && r.opts.UnderMaintenance(ctx, m, agora) {
			continue
		}

		// A marca vem antes do envio, e o envio só acontece se ela passou:
		// um reconhecimento gravado depois da leitura acima ganha do
		// lembrete, em vez de a pessoa receber um lembrete logo depois de
		// dizer que estava olhando.
		marcou, err := e.store.Incidents().MarkReminded(ctx, inc.ID, inc.RemindedAt, agora)
		if err != nil {
			slog.Error("falha ao registrar lembrete", "monitor", m.Name, "erro", err)
			continue
		}
		if !marcou {
			continue
		}
		e.remind(ctx, m, inc, agora, arvore)
	}
}

// openIncidents lê todas as quedas abertas, página a página.
func (r *Reminders) openIncidents(ctx context.Context) ([]domain.Incident, error) {
	var (
		todos  []domain.Incident
		filtro = store.IncidentFilter{OnlyOpen: true, Limit: store.MaxPageSize}
	)
	for {
		pagina, err := r.engine.store.Incidents().List(ctx, filtro)
		if err != nil {
			return nil, err
		}
		todos = append(todos, pagina.Items...)
		if !pagina.HasMore || len(pagina.Items) == 0 {
			return todos, nil
		}
		filtro.BeforeID = pagina.Items[len(pagina.Items)-1].ID
	}
}

// reminderTarget devolve o monitor cuja queda aberta merece lembrete.
//
// Pausado não: quem pausou sabe. Fora da queda confirmada também não — o
// incidente aberto com o monitor no ar é registro que o banco não
// conseguiu encerrar, e lembrar dele seria mentir. E o monitor calado por
// um ancestral em queda fica de fora como ficou do aviso: a causa é o
// ancestral, e o lembrete dele já diz quantos dependentes estão calados.
func (e *Engine) reminderTarget(id int64) (domain.Monitor, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	m, ok := e.monitors[id]
	if !ok || !m.Enabled || e.states[id].Status != domain.StatusDown {
		return domain.Monitor{}, false
	}
	if _, calado := domain.DownAncestor(m, e.lookup, e.stateOf); calado {
		return domain.Monitor{}, false
	}
	return m, true
}

// remind entrega o lembrete pelos mesmos canais e pela mesma fila do aviso
// de queda.
func (e *Engine) remind(ctx context.Context, m domain.Monitor, inc domain.Incident, at time.Time, grupos domain.GroupTree) {
	destinos := e.destinations(ctx, m)
	if len(destinos) == 0 {
		return
	}

	grupo := ""
	if m.GroupID != nil {
		grupo = grupos.Path(*m.GroupID)
	}
	e.dispatch.Enqueue(notifier.Notification{
		Monitor: m,
		Event: Event{
			Kind: KindDown, From: domain.StatusDown, To: domain.StatusDown,
			At: at, Duration: at.Sub(inc.StartedAt),
		},
		Message:    inc.Cause,
		Group:      grupo,
		Dependents: e.dependents(m.ID),
		Reminder:   true,
	}, destinos)
}

// destinations monta os notificadores dos canais do monitor. Canal mal
// configurado fica de fora sem calar os outros, como no aviso de queda.
func (e *Engine) destinations(ctx context.Context, m domain.Monitor) []notifier.Notifier {
	canais, err := e.store.Channels().Destinations(ctx, m.ID)
	if err != nil {
		slog.Error("falha ao listar canais do monitor", "monitor", m.Name, "erro", err)
		return nil
	}

	destinos := make([]notifier.Notifier, 0, len(canais))
	for _, canal := range canais {
		n, err := notifier.Build(canal.Type, canal.Config)
		if err != nil {
			slog.Error("canal de aviso inválido",
				"canal", canal.Name, "tipo", canal.Type, "erro", err)
			continue
		}
		destinos = append(destinos, n)
	}
	return destinos
}
