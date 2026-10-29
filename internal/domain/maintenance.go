package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Janelas de manutenção.
//
// "Entre 19h e 19h30 o RPA/prod vai cair de propósito." Sem como dizer
// isso, todo deploy acendia o aviso do time inteiro, e quem recebe alarme
// em toda manutenção planejada aprende a ignorar o canal. Desligar o
// monitor resolvia pela metade: alguém precisava lembrar de religar, e o
// histórico sumia em vez de registrar a manutenção.
//
// A janela não desliga nada. As verificações continuam e são gravadas; a
// falha observada dentro dela vira "sem medição", com o motivo na
// mensagem — a mesma saída da sentinela de rede e do pai fora do ar. Por
// isso não abre incidente, não avisa, e a disponibilidade não a conta
// como queda: o intervalo fica como lacuna.

const (
	maxMaintenanceTitleLength   = 160
	maxMaintenanceMessageLength = 2000

	// maxMaintenanceDuration limita a janela avulsa. Uma janela esquecida
	// cala os avisos como o monitor desligado que se esqueceu de religar —
	// o problema que ela veio resolver. Uma migração longa cabe com folga;
	// "manutenção até segunda ordem" não.
	maxMaintenanceDuration = 31 * 24 * time.Hour
)

// Recurrence é como a janela se repete.
type Recurrence string

const (
	// RecurrenceNone é a janela avulsa: acontece uma vez.
	RecurrenceNone Recurrence = "none"
	// RecurrenceDaily repete no mesmo horário todo dia.
	RecurrenceDaily Recurrence = "daily"
	// RecurrenceWeekly repete no mesmo horário e dia da semana.
	RecurrenceWeekly Recurrence = "weekly"
)

// period é o passo da repetição em dias; zero para a avulsa.
func (r Recurrence) period() int {
	switch r {
	case RecurrenceDaily:
		return 1
	case RecurrenceWeekly:
		return 7
	default:
		return 0
	}
}

// Valid informa se a recorrência pertence ao conjunto conhecido.
func (r Recurrence) Valid() bool {
	return r == RecurrenceNone || r == RecurrenceDaily || r == RecurrenceWeekly
}

// MaintenanceWindow é uma janela de manutenção declarada.
//
// O alvo é um conjunto de monitores e de grupos. Grupo vale em cascata:
// a janela no sistema "RPA" cobre o RPA/hml e o RPA/prod, inclusive os
// monitores criados depois. E a janela vale para os dependentes (#36): o
// monitor cujo pai está em manutenção está em manutenção também, porque
// a queda dele é a do pai — a mesma regra que já o cala quando o pai cai.
type MaintenanceWindow struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`

	// Message é o texto que a página pública mostra junto do título.
	Message string `json:"message,omitempty"`

	// StartsAt e EndsAt são a primeira ocorrência. Na recorrente, as
	// seguintes repetem o mesmo horário de parede no fuso da janela.
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     time.Time  `json:"ends_at"`
	Recurrence Recurrence `json:"recurrence"`

	// TimeZone é o fuso em que a recorrência repete o horário. Sem ele,
	// "todo domingo às 2h" escorregaria uma hora na troca de horário de
	// verão, e o domingo começaria no sábado de quem está a oeste de UTC.
	TimeZone string `json:"time_zone"`

	MonitorIDs []int64 `json:"monitor_ids"`
	GroupIDs   []int64 `json:"group_ids"`

	// EndedAt é o encerramento antes do previsto. Daí em diante a janela
	// não cobre mais nada, nem as próximas ocorrências da recorrente.
	EndedAt *time.Time `json:"ended_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaintenanceOccurrence é uma ocorrência concreta da janela.
type MaintenanceOccurrence struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// MaintenanceState resume a janela num instante.
type MaintenanceState string

const (
	MaintenanceScheduled MaintenanceState = "scheduled"
	MaintenanceActive    MaintenanceState = "active"
	MaintenanceFinished  MaintenanceState = "finished"
)

// Validate confere as invariantes da janela.
func (w MaintenanceWindow) Validate() error {
	title := strings.TrimSpace(w.Title)
	if title == "" {
		return invalid("title", "não pode ser vazio")
	}
	if len(title) > maxMaintenanceTitleLength {
		return invalid("title", fmt.Sprintf("não pode passar de %d caracteres", maxMaintenanceTitleLength))
	}
	if len(w.Message) > maxMaintenanceMessageLength {
		return invalid("message", fmt.Sprintf("não pode passar de %d caracteres", maxMaintenanceMessageLength))
	}
	if w.StartsAt.IsZero() {
		return invalid("starts_at", "obrigatório")
	}
	if !w.EndsAt.After(w.StartsAt) {
		return invalid("ends_at", "precisa ser depois do início")
	}
	if !w.Recurrence.Valid() {
		return invalid("recurrence", "use none, daily ou weekly")
	}
	if _, err := time.LoadLocation(w.TimeZone); err != nil || w.TimeZone == "" {
		return invalid("time_zone", "fuso horário desconhecido")
	}

	dur := w.EndsAt.Sub(w.StartsAt)
	if p := w.Recurrence.period(); p > 0 {
		// Uma ocorrência que encosta na seguinte é manutenção permanente
		// com outro nome.
		if dur >= time.Duration(p)*24*time.Hour {
			return invalid("ends_at", "a janela precisa ser mais curta que o intervalo da repetição")
		}
	} else if dur > maxMaintenanceDuration {
		return invalid("ends_at", "uma janela avulsa dura no máximo 31 dias")
	}

	if len(w.MonitorIDs) == 0 && len(w.GroupIDs) == 0 {
		return invalid("monitor_ids", "escolha ao menos um monitor ou grupo")
	}
	return nil
}

// location devolve o fuso da janela. Validate recusou o desconhecido na
// entrada; se um chegar aqui, UTC é melhor que deixar de silenciar.
func (w MaintenanceWindow) location() *time.Location {
	loc, err := time.LoadLocation(w.TimeZone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// occurrence devolve a k-ésima ocorrência, já cortada pelo encerramento.
// ok falso quando ela não chega a acontecer.
func (w MaintenanceWindow) occurrence(k int) (MaintenanceOccurrence, bool) {
	inicio := w.StartsAt
	if k > 0 {
		// AddDate no fuso da janela mantém o horário de parede através da
		// troca de horário de verão; somar 24h não manteria.
		inicio = w.StartsAt.In(w.location()).AddDate(0, 0, k*w.Recurrence.period())
	}
	o := MaintenanceOccurrence{Start: inicio.UTC(), End: inicio.Add(w.EndsAt.Sub(w.StartsAt)).UTC()}

	if w.EndedAt != nil {
		if !o.Start.Before(*w.EndedAt) {
			return MaintenanceOccurrence{}, false
		}
		if o.End.After(*w.EndedAt) {
			o.End = w.EndedAt.UTC()
		}
	}
	return o, true
}

// candidates são os índices de ocorrência perto de t. A conta pela
// duração nominal do período erra por uma hora na troca de horário, e
// olhar o vizinho de cada lado cobre o erro.
func (w MaintenanceWindow) candidates(t time.Time) []int {
	p := w.Recurrence.period()
	if p == 0 {
		return []int{0}
	}
	k := int(t.Sub(w.StartsAt) / (time.Duration(p) * 24 * time.Hour))
	out := make([]int, 0, 4)
	for i := k - 1; i <= k+2; i++ {
		if i >= 0 {
			out = append(out, i)
		}
	}
	return out
}

// OccurrenceAt devolve a ocorrência em curso no instante, se houver.
func (w MaintenanceWindow) OccurrenceAt(t time.Time) (MaintenanceOccurrence, bool) {
	if t.Before(w.StartsAt) {
		return MaintenanceOccurrence{}, false
	}
	for _, k := range w.candidates(t) {
		o, ok := w.occurrence(k)
		if ok && !t.Before(o.Start) && t.Before(o.End) {
			return o, true
		}
	}
	return MaintenanceOccurrence{}, false
}

// ActiveAt informa se a janela está em curso no instante.
func (w MaintenanceWindow) ActiveAt(t time.Time) bool {
	_, ok := w.OccurrenceAt(t)
	return ok
}

// NextOccurrence devolve a ocorrência em curso ou a próxima depois de t.
func (w MaintenanceWindow) NextOccurrence(t time.Time) (MaintenanceOccurrence, bool) {
	if t.Before(w.StartsAt) {
		return w.occurrence(0)
	}
	for _, k := range w.candidates(t) {
		o, ok := w.occurrence(k)
		if !ok {
			// Encerrada antes desta: as seguintes também não acontecem.
			return MaintenanceOccurrence{}, false
		}
		if o.End.After(t) {
			return o, true
		}
	}
	return MaintenanceOccurrence{}, false
}

// StateAt resume a janela no instante.
func (w MaintenanceWindow) StateAt(t time.Time) MaintenanceState {
	if w.ActiveAt(t) {
		return MaintenanceActive
	}
	if _, ok := w.NextOccurrence(t); ok {
		return MaintenanceScheduled
	}
	return MaintenanceFinished
}

// targets informa se a janela aponta para o monitor diretamente: por ele,
// pelo grupo dele ou pelo sistema acima do grupo.
func (w MaintenanceWindow) targets(m Monitor, tree GroupTree) bool {
	if slices.Contains(w.MonitorIDs, m.ID) {
		return true
	}
	if m.GroupID == nil {
		return false
	}
	if slices.Contains(w.GroupIDs, *m.GroupID) {
		return true
	}
	g, ok := tree.Get(*m.GroupID)
	return ok && g.ParentID != nil && slices.Contains(w.GroupIDs, *g.ParentID)
}

// Covers informa se a janela vale para o monitor, sem olhar o relógio:
// pelo próprio monitor ou por um ancestral de dependência.
func (w MaintenanceWindow) Covers(m Monitor, lookup MonitorLookup, tree GroupTree) bool {
	vistos := map[int64]bool{}
	for atual, ok := m, true; ok && !vistos[atual.ID]; {
		vistos[atual.ID] = true
		if w.targets(atual, tree) {
			return true
		}
		if atual.ParentID == nil {
			return false
		}
		atual, ok = lookup(*atual.ParentID)
	}
	return false
}

// MaintenanceFor devolve a janela que deixa o monitor em manutenção no
// instante, se houver.
//
// É a única resposta para "este monitor está em manutenção agora?": o
// motor a usa para não avisar, e a página pública para dizer
// "manutenção" em vez de queda. Duas regras divergiriam na primeira
// mudança, e a página diria manutenção enquanto o pager tocava.
func MaintenanceFor(
	m Monitor,
	lookup MonitorLookup,
	windows []MaintenanceWindow,
	tree GroupTree,
	at time.Time,
) (MaintenanceWindow, bool) {
	for _, w := range windows {
		if w.ActiveAt(at) && w.Covers(m, lookup, tree) {
			return w, true
		}
	}
	return MaintenanceWindow{}, false
}

// PublicMaintenance é a janela como a página pública a mostra: o título e
// o texto que o administrador escreveu, a ocorrência em curso ou a
// próxima, e os componentes desta página que ela afeta — pelo rótulo,
// nunca pelo identificador.
type PublicMaintenance struct {
	Title      string     `json:"title"`
	Message    string     `json:"message,omitempty"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     time.Time  `json:"ends_at"`
	Recurrence Recurrence `json:"recurrence"`
	Active     bool       `json:"active"`
	Components []string   `json:"components"`
}
