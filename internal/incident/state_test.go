package incident_test

import (
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/incident"
)

var epoch = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

const threshold = 3

// feed alimenta a máquina com uma sequência de observações, uma por
// minuto, e devolve o estado final com todos os eventos emitidos.
func feed(start incident.State, statuses ...domain.Status) (incident.State, []incident.Event) {
	cfg := incident.Config{Threshold: threshold}
	state := start

	var todos []incident.Event
	for i, status := range statuses {
		at := epoch.Add(time.Duration(i) * time.Minute)

		next, eventos := incident.Next(state, status, at, cfg)
		state = next
		todos = append(todos, eventos...)
	}
	return state, todos
}

// repeat produz n observações do mesmo estado.
func repeat(n int, status domain.Status) []domain.Status {
	out := make([]domain.Status, n)
	for i := range out {
		out[i] = status
	}
	return out
}

func kinds(events []incident.Event) []incident.Kind {
	out := make([]incident.Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// ---------- confirmação ----------

// Alertar na primeira falha transformaria qualquer soluço de rede em
// chamado noturno.
func TestBelowThresholdDoesNotConfirmFailure(t *testing.T) {
	state, events := feed(incident.State{}, repeat(threshold-1, domain.StatusDown)...)

	if state.Status == domain.StatusDown {
		t.Errorf("Status = %v after %d failures, want it unconfirmed", state.Status, threshold-1)
	}
	if len(events) != 0 {
		t.Errorf("emitted %v, want no event before the threshold", kinds(events))
	}
}

func TestThresholdConfirmsFailureAndOpensIncident(t *testing.T) {
	state, events := feed(incident.State{}, repeat(threshold, domain.StatusDown)...)

	if state.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusDown)
	}
	if got := kinds(events); len(got) != 1 || got[0] != incident.KindDown {
		t.Fatalf("emitted %v, want a single %v", got, incident.KindDown)
	}
	if !events[0].At.Equal(epoch.Add(time.Duration(threshold-1) * time.Minute)) {
		t.Errorf("event at %v, want the instant of the confirming observation", events[0].At)
	}
}

// Continuar fora do ar não é notícia nova: repetir o alerta a cada
// verificação afogaria o canal durante uma queda longa.
func TestFurtherFailuresDoNotRepeatTheAlert(t *testing.T) {
	_, events := feed(incident.State{}, repeat(threshold+10, domain.StatusDown)...)

	if got := kinds(events); len(got) != 1 {
		t.Errorf("emitted %v, want exactly one event for a sustained outage", got)
	}
}

// Uma resposta isolada no meio da queda não significa recuperação; exigir
// a mesma confirmação evita anunciar volta que não aconteceu.
func TestSingleSuccessDoesNotResolveTheIncident(t *testing.T) {
	sequencia := append(repeat(threshold, domain.StatusDown), domain.StatusUp)

	state, events := feed(incident.State{}, sequencia...)

	if state.Status != domain.StatusDown {
		t.Errorf("Status = %v after one success, want it still %v", state.Status, domain.StatusDown)
	}
	if got := kinds(events); len(got) != 1 {
		t.Errorf("emitted %v, want the resolution to still be unconfirmed", got)
	}
}

func TestThresholdSuccessesResolveTheIncident(t *testing.T) {
	sequencia := append(repeat(threshold, domain.StatusDown), repeat(threshold, domain.StatusUp)...)

	state, events := feed(incident.State{}, sequencia...)

	if state.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusUp)
	}
	got := kinds(events)
	if len(got) != 2 || got[0] != incident.KindDown || got[1] != incident.KindUp {
		t.Fatalf("emitted %v, want [%v %v]", got, incident.KindDown, incident.KindUp)
	}
}

// Saber quanto tempo durou é a primeira pergunta depois de "voltou?", e
// calculá-la de cabeça a partir de dois horários é trabalho que a
// ferramenta deveria poupar.
func TestResolutionCarriesTheOutageDuration(t *testing.T) {
	sequencia := append(repeat(threshold, domain.StatusDown), repeat(threshold, domain.StatusUp)...)

	_, events := feed(incident.State{}, sequencia...)

	resolucao := events[len(events)-1]
	if resolucao.Kind != incident.KindUp {
		t.Fatalf("last event is %v, want %v", resolucao.Kind, incident.KindUp)
	}

	// A queda foi confirmada na observação 3 e a volta na observação 6,
	// uma por minuto.
	if want := 3 * time.Minute; resolucao.Duration != want {
		t.Errorf("Duration = %v, want %v", resolucao.Duration, want)
	}
}

// Alternar entre no ar e fora do ar sem completar o limiar é exatamente o
// caso que o limiar existe para silenciar.
func TestFlappingNeverConfirms(t *testing.T) {
	var sequencia []domain.Status
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			sequencia = append(sequencia, domain.StatusDown)
			continue
		}
		sequencia = append(sequencia, domain.StatusUp)
	}

	_, events := feed(incident.State{}, sequencia...)

	if len(events) != 0 {
		t.Errorf("emitted %v while flapping, want silence", kinds(events))
	}
}

// Limiar 1 é para quem prefere ser avisado na hora e aceita o ruído.
func TestThresholdOfOneAlertsImmediately(t *testing.T) {
	state, events := incident.Next(
		incident.State{}, domain.StatusDown, epoch, incident.Config{Threshold: 1})

	if state.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusDown)
	}
	if got := kinds(events); len(got) != 1 || got[0] != incident.KindDown {
		t.Errorf("emitted %v, want a single %v", got, incident.KindDown)
	}
}

// Limiar ausente ou absurdo não pode desligar a confirmação nem travá-la.
func TestInvalidThresholdFallsBackToOne(t *testing.T) {
	for _, valor := range []int{0, -5} {
		state, events := incident.Next(
			incident.State{}, domain.StatusDown, epoch, incident.Config{Threshold: valor})

		if state.Status != domain.StatusDown || len(events) != 1 {
			t.Errorf("threshold %d produced status %v and %v; want it to behave as 1",
				valor, state.Status, kinds(events))
		}
	}
}

// ---------- degradado ----------

func TestDegradedIsConfirmedLikeAnyOtherState(t *testing.T) {
	state, events := feed(incident.State{}, repeat(threshold, domain.StatusDegraded)...)

	if state.Status != domain.StatusDegraded {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusDegraded)
	}
	if got := kinds(events); len(got) != 1 || got[0] != incident.KindDegraded {
		t.Errorf("emitted %v, want a single %v", got, incident.KindDegraded)
	}
}

// Degradar durante uma queda é agravamento, não recuperação: o alvo
// voltou a responder, e isso merece aviso próprio.
func TestDegradedAfterOutageResolvesTheIncident(t *testing.T) {
	sequencia := append(repeat(threshold, domain.StatusDown), repeat(threshold, domain.StatusDegraded)...)

	state, events := feed(incident.State{}, sequencia...)

	if state.Status != domain.StatusDegraded {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusDegraded)
	}
	ultimo := events[len(events)-1]
	if ultimo.Kind != incident.KindDegraded {
		t.Errorf("last event = %v, want %v", ultimo.Kind, incident.KindDegraded)
	}
	if ultimo.Duration == 0 {
		t.Error("the event carries no outage duration; the incident ended here")
	}
}

// ---------- observações sem medição ----------

// A sentinela converte falha em desconhecido quando a rede do próprio
// monitor caiu. Deixar isso mover a máquina anularia a supressão: o
// alerta chegaria assim mesmo, só que rotulado de outro jeito.
func TestUnknownDoesNotAdvanceTheMachine(t *testing.T) {
	confirmado, _ := feed(incident.State{}, repeat(threshold, domain.StatusUp)...)

	depois, events := feed(confirmado, repeat(10, domain.StatusUnknown)...)

	if depois.Status != domain.StatusUp {
		t.Errorf("Status = %v after unmeasured samples, want it unchanged at %v",
			depois.Status, domain.StatusUp)
	}
	if len(events) != 0 {
		t.Errorf("emitted %v for unmeasured samples, want silence", kinds(events))
	}
}

// Desconhecido no meio de uma contagem não pode zerá-la: a rede voltou e
// as falhas anteriores continuam valendo.
func TestUnknownDoesNotResetAPendingCount(t *testing.T) {
	sequencia := []domain.Status{
		domain.StatusDown,
		domain.StatusDown,
		domain.StatusUnknown,
		domain.StatusDown, // esta completa o limiar de três
	}

	state, events := feed(incident.State{}, sequencia...)

	if state.Status != domain.StatusDown {
		t.Errorf("Status = %v, want the pending count to have survived the gap", state.Status)
	}
	if got := kinds(events); len(got) != 1 || got[0] != incident.KindDown {
		t.Errorf("emitted %v, want a single %v", got, incident.KindDown)
	}
}

// ---------- primeira observação ----------

// Um monitor recém-criado que já nasce fora do ar precisa alertar; o
// estado inicial desconhecido não é uma queda a partir de nada.
func TestFirstObservationsConfirmNormally(t *testing.T) {
	state, events := feed(incident.State{}, repeat(threshold, domain.StatusUp)...)

	if state.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusUp)
	}
	// Confirmar que está no ar pela primeira vez não é motivo de alerta.
	if len(events) != 0 {
		t.Errorf("emitted %v on first coming up, want silence", kinds(events))
	}
}

func TestFirstConfirmedFailureAlerts(t *testing.T) {
	_, events := feed(incident.State{}, repeat(threshold, domain.StatusDown)...)

	if got := kinds(events); len(got) != 1 || got[0] != incident.KindDown {
		t.Errorf("emitted %v, want a monitor that starts down to alert", got)
	}
}

// ---------- estado ----------

func TestStateRecordsWhenTheStatusChanged(t *testing.T) {
	state, _ := feed(incident.State{}, repeat(threshold, domain.StatusDown)...)

	if want := epoch.Add(time.Duration(threshold-1) * time.Minute); !state.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", state.Since, want)
	}
}

func TestStateTracksThePendingCandidate(t *testing.T) {
	state, _ := feed(incident.State{}, domain.StatusUp, domain.StatusUp, domain.StatusUp, domain.StatusDown)

	if state.Candidate != domain.StatusDown {
		t.Errorf("Candidate = %v, want %v", state.Candidate, domain.StatusDown)
	}
	if state.Consecutive != 1 {
		t.Errorf("Consecutive = %d, want 1", state.Consecutive)
	}
}

// Voltar ao estado confirmado descarta a contagem pendente.
func TestReturningToConfirmedStatusClearsTheCandidate(t *testing.T) {
	state, _ := feed(incident.State{},
		domain.StatusUp, domain.StatusUp, domain.StatusUp, // confirma no ar
		domain.StatusDown, domain.StatusDown, // dois pendentes
		domain.StatusUp, // volta ao confirmado
	)

	if state.Consecutive != 0 {
		t.Errorf("Consecutive = %d, want the pending count cleared", state.Consecutive)
	}
	if state.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", state.Status, domain.StatusUp)
	}
}

// Next não pode alterar o estado recebido: o chamador compara o antes com
// o depois para decidir o que persistir.
func TestNextDoesNotMutateTheInputState(t *testing.T) {
	antes := incident.State{
		Status: domain.StatusUp, Candidate: domain.StatusDown,
		Consecutive: 2, Since: epoch,
	}
	copia := antes

	incident.Next(antes, domain.StatusDown, epoch.Add(time.Minute), incident.Config{Threshold: threshold})

	if antes != copia {
		t.Errorf("Next changed the input state from %+v to %+v", copia, antes)
	}
}
