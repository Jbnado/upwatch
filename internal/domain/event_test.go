package domain

import "testing"

// A mudança confirmada de estado vira o tipo de evento que o canal assina.
// É o que liga a família de monitor ao filtro de canal sem o motor
// precisar conhecer os nomes dos tipos.
func TestChangeKindEventType(t *testing.T) {
	casos := map[ChangeKind]EventType{
		ChangeDown:     EventMonitorDown,
		ChangeUp:       EventMonitorUp,
		ChangeDegraded: EventMonitorDegraded,
	}
	for k, want := range casos {
		if got := k.EventType(); got != want {
			t.Errorf("ChangeKind(%v).EventType() = %q, want %q", k, got, want)
		}
	}
}

// Conjunto vazio recebe tudo: é o canal de quem nunca configurou o filtro,
// e precisa continuar recebendo cada transição — inclusive tipos de
// famílias que ainda nem existem, para a #128 não precisar migrar canal
// nenhum ao acrescentar a família de conta.
func TestChannelWantsEventEmptyReceivesEverything(t *testing.T) {
	c := Channel{}
	for _, e := range []EventType{EventMonitorDown, EventMonitorUp, EventMonitorDegraded, "account.token_expiring"} {
		if !c.WantsEvent(e) {
			t.Errorf("canal sem filtro não quer %q, want receber tudo", e)
		}
	}
}

// Conjunto não vazio recebe exatamente o que lista.
func TestChannelWantsEventFiltersBySet(t *testing.T) {
	c := Channel{Events: []EventType{EventMonitorDown}}
	if !c.WantsEvent(EventMonitorDown) {
		t.Error("canal que assina a queda não a quer")
	}
	for _, e := range []EventType{EventMonitorUp, EventMonitorDegraded, "account.token_expiring"} {
		if c.WantsEvent(e) {
			t.Errorf("canal que só assina a queda quer %q", e)
		}
	}
}

// A família de conta é assinável e não tem evento obrigatório: um canal
// pode escolher receber só os eventos de token, sem a queda de monitor —
// ao contrário da família de monitor, que exige a queda. É o que a #128
// acrescenta reusando o mecanismo do #8.
func TestChannelValidateAcceptsAccountEvents(t *testing.T) {
	casos := [][]EventType{
		{EventAccountTokenExpiring},
		{EventAccountTokenExpiring, EventAccountTokenRevoked},
		{EventMonitorDown, EventAccountTokenExpiring},
	}
	for _, events := range casos {
		c := Channel{Name: "plantão", Type: "webhook", Events: events}
		if err := c.Validate(); err != nil {
			t.Errorf("Validate recusou os eventos de conta %v: %v", events, err)
		}
	}
}

// Os eventos de conta são conhecidos: assinar account.token_expiring não é
// erro de digitação que calaria o canal em silêncio.
func TestKnownEventAccount(t *testing.T) {
	for _, e := range []EventType{EventAccountTokenExpiring, EventAccountTokenRevoked} {
		if !KnownEvent(e) {
			t.Errorf("KnownEvent(%q) = false, want true", e)
		}
	}
}

// Um canal que assina só a queda não recebe os eventos de conta: conjunto
// não vazio recebe exatamente o que lista.
func TestChannelWantsEventAccountFiltered(t *testing.T) {
	c := Channel{Events: []EventType{EventMonitorDown}}
	if c.WantsEvent(EventAccountTokenExpiring) {
		t.Error("canal que só assina a queda recebeu evento de conta")
	}
}

// Filtrar que esconde a queda é pior que canal nenhum: o alerta some
// justamente quando mais importa. Um canal que escolhe eventos da família
// de monitor precisa manter a queda.
func TestChannelValidateRejectsMonitorFilterWithoutDown(t *testing.T) {
	c := Channel{Name: "plantão", Type: "webhook", Events: []EventType{EventMonitorUp, EventMonitorDegraded}}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate aceitou um filtro de monitor sem a queda")
	}
}

// Vazio (todos) e um conjunto que inclui a queda são válidos.
func TestChannelValidateAcceptsSafeFilters(t *testing.T) {
	ok := [][]EventType{
		nil,
		{EventMonitorDown},
		{EventMonitorDown, EventMonitorUp, EventMonitorDegraded},
	}
	for _, events := range ok {
		c := Channel{Name: "plantão", Type: "webhook", Events: events}
		if err := c.Validate(); err != nil {
			t.Errorf("Validate recusou o filtro seguro %v: %v", events, err)
		}
	}
}

// Um tipo que não pertence a nenhuma família é erro de digitação, não um
// canal que recebe nada em silêncio.
func TestChannelValidateRejectsUnknownEvent(t *testing.T) {
	c := Channel{Name: "plantão", Type: "webhook", Events: []EventType{EventMonitorDown, "monitor.explodiu"}}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate aceitou um tipo de evento desconhecido")
	}
}
