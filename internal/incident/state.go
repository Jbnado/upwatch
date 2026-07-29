// Package incident decide quando uma sequência de verificações vira
// notícia.
//
// A regra é pequena e cara de errar: alertar cedo demais enche o canal de
// falso positivo até as pessoas silenciarem o alerta, e alertar tarde
// demais faz a ferramenta perder a serventia. Por isso a decisão vive
// numa função pura, verificável em tabela sem banco nem relógio.
package incident

import (
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// Kind e Event são apelidos dos tipos do domínio.
//
// A transição é o vocabulário comum entre quem a decide, aqui, e quem a
// comunica, no pacote de avisos. Defini-la num dos dois obrigaria o outro
// a importá-lo — e como o motor precisa dos dois, viraria ciclo.
type (
	Kind  = domain.ChangeKind
	Event = domain.StateChange
)

const (
	KindDown     = domain.ChangeDown
	KindUp       = domain.ChangeUp
	KindDegraded = domain.ChangeDegraded
)

// Config são os parâmetros da confirmação.
type Config struct {
	// Threshold é quantas observações seguidas confirmam uma mudança.
	Threshold int
}

func (c Config) threshold() int {
	if c.Threshold < 1 {
		// Valor ausente ou absurdo não pode desligar a confirmação nem
		// travá-la; um é o comportamento mais previsível.
		return 1
	}
	return c.Threshold
}

// State é o que precisa ser lembrado entre verificações.
//
// Apelido do tipo do domínio: a máquina o opera, e o store o persiste,
// sem que nenhum dos dois precise conhecer o outro.
type State = domain.MonitorState

// Next processa uma observação e devolve o novo estado com os eventos.
//
// Não altera o estado recebido: quem chama compara o antes com o depois
// para decidir o que persistir.
func Next(s State, observed domain.Status, at time.Time, cfg Config) (State, []Event) {
	// Observação sem medição não move nada. É o resultado que a sentinela
	// produz quando a rede do próprio monitor caiu; deixá-la avançar a
	// máquina anularia a supressão, porque o alerta chegaria assim mesmo,
	// só que rotulado de outro jeito. Também não zera a contagem
	// pendente: as falhas anteriores continuam valendo.
	if observed == domain.StatusUnknown {
		return s, nil
	}

	// Voltou ao estado confirmado: descarta a contagem pendente.
	if observed == s.Status {
		s.Candidate = domain.StatusUnknown
		s.Consecutive = 0
		return s, nil
	}

	if observed == s.Candidate {
		s.Consecutive++
	} else {
		s.Candidate = observed
		s.Consecutive = 1
	}

	if s.Consecutive < cfg.threshold() {
		return s, nil
	}

	return confirm(s, observed, at)
}

// confirm aplica a mudança e produz o evento correspondente.
func confirm(s State, observed domain.Status, at time.Time) (State, []Event) {
	anterior := s.Status
	desde := s.Since

	s.Status = observed
	s.Candidate = domain.StatusUnknown
	s.Consecutive = 0
	s.Since = at

	// Confirmar pela primeira vez que o alvo está no ar não é notícia:
	// avisar "seu serviço subiu" a cada cadastro seria ruído.
	if anterior == domain.StatusUnknown && observed == domain.StatusUp {
		return s, nil
	}

	evento := Event{
		Kind: kindOf(observed),
		From: anterior,
		To:   observed,
		At:   at,
	}
	if !desde.IsZero() {
		evento.Duration = at.Sub(desde)
	}

	return s, []Event{evento}
}

func kindOf(status domain.Status) Kind {
	switch status {
	case domain.StatusDown:
		return KindDown
	case domain.StatusDegraded:
		return KindDegraded
	default:
		return KindUp
	}
}
