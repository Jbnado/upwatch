package domain

import "slices"

// EventType identifica um tipo de evento que um canal de aviso pode
// receber. É nomeado por família — "<família>.<evento>" — de propósito: o
// filtro de canal cresce sem reescrever o modelo. Hoje existe a família
// "monitor" (queda, volta, degradação); a assinatura de token da #128
// acrescenta a família "account" declarando tipos novos em eventFamilies,
// e o canal passa a poder assiná-los sem mudança no armazenamento nem no
// motor — o conjunto guarda tipos opacos, e o filtro pergunta "quer este?"
// sem conhecer as famílias.
type EventType string

const (
	// EventMonitorDown é a queda confirmada de um monitor.
	EventMonitorDown EventType = "monitor.down"
	// EventMonitorUp é a volta confirmada.
	EventMonitorUp EventType = "monitor.up"
	// EventMonitorDegraded é a degradação confirmada: responde, porém fora
	// do esperado.
	EventMonitorDegraded EventType = "monitor.degraded"
)

// eventFamily descreve uma família de eventos para o filtro de canal.
type eventFamily struct {
	// types são os tipos da família, na ordem de exibição.
	types []EventType
	// required é o tipo que um canal não pode esconder se filtra a família:
	// vazio quando a família não tem evento obrigatório. Na família de
	// monitor é a queda — filtrar escondendo a queda é pior que canal
	// nenhum, porque o alerta some justamente quando mais importa.
	required EventType
}

// eventFamilies registra as famílias conhecidas. A #128 acrescenta aqui a
// família de conta (token vencendo, token revogado) e o filtro passa a
// aceitá-la sem outra mudança.
var eventFamilies = map[string]eventFamily{
	"monitor": {
		types:    []EventType{EventMonitorDown, EventMonitorUp, EventMonitorDegraded},
		required: EventMonitorDown,
	},
}

// EventType mapeia a mudança confirmada de estado para o tipo de evento
// que um canal assina. Devolve vazio para uma mudança que não corresponde
// a nenhum tipo — o filtro trata vazio como "não é desta família".
func (k ChangeKind) EventType() EventType {
	switch k {
	case ChangeDown:
		return EventMonitorDown
	case ChangeUp:
		return EventMonitorUp
	case ChangeDegraded:
		return EventMonitorDegraded
	}
	return ""
}

// KnownEvent informa se o tipo pertence a alguma família registrada.
//
// O filtro recusa o que não reconhece para um erro de digitação na API
// não virar um canal que não recebe nada em silêncio.
func KnownEvent(t EventType) bool {
	for _, fam := range eventFamilies {
		if slices.Contains(fam.types, t) {
			return true
		}
	}
	return false
}

// validateEventFilter confere o conjunto assinado por um canal.
//
// Vazio recebe tudo e é válido. Um conjunto não vazio precisa ser de tipos
// conhecidos, e, para cada família que ele toca, precisa manter o evento
// obrigatório dela (na de monitor, a queda). Famílias futuras entram em
// eventFamilies com a própria regra, sem mexer nas existentes.
func validateEventFilter(events []EventType) error {
	if len(events) == 0 {
		return nil
	}
	for _, e := range events {
		if !KnownEvent(e) {
			return invalid("events", "evento desconhecido: "+string(e))
		}
	}
	for nome, fam := range eventFamilies {
		if fam.required == "" || !tocaFamilia(events, fam) {
			continue
		}
		if !slices.Contains(events, fam.required) {
			return invalid("events", "um canal que filtra eventos de "+nome+
				" precisa manter "+string(fam.required)+"; esconder a queda é pior que não avisar")
		}
	}
	return nil
}

// tocaFamilia informa se o conjunto assina algum tipo da família.
func tocaFamilia(events []EventType, fam eventFamily) bool {
	for _, t := range fam.types {
		if slices.Contains(events, t) {
			return true
		}
	}
	return false
}
