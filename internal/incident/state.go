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

// Kind identifica o que aconteceu.
type Kind uint8

const (
	// KindDown é a confirmação de que o alvo está fora do ar.
	KindDown Kind = iota + 1
	// KindUp é a volta confirmada.
	KindUp
	// KindDegraded é a confirmação de que o alvo responde, porém fora do
	// esperado.
	KindDegraded
)

var kindNames = map[Kind]string{
	KindDown:     "down",
	KindUp:       "up",
	KindDegraded: "degraded",
}

func (k Kind) String() string {
	if nome, ok := kindNames[k]; ok {
		return nome
	}
	return "unknown"
}

// Event é uma mudança confirmada de estado.
type Event struct {
	Kind Kind
	// From é o estado anterior confirmado.
	From domain.Status
	// To é o estado que passou a valer.
	To domain.Status
	// At é o instante da observação que confirmou a mudança.
	At time.Time
	// Duration é quanto durou o estado anterior. Responde à primeira
	// pergunta depois de "voltou?", sem obrigar ninguém a subtrair dois
	// horários de cabeça.
	Duration time.Duration
}

// Resolves informa se o evento encerra uma indisponibilidade.
func (e Event) Resolves() bool {
	return e.From == domain.StatusDown && e.To != domain.StatusDown
}

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
type State struct {
	// Status é o estado confirmado, o que vale para alerta e para a
	// interface.
	Status domain.Status
	// Candidate é o estado que está tentando se confirmar.
	Candidate domain.Status
	// Consecutive é quantas observações seguidas o candidato acumulou.
	Consecutive int
	// Since é desde quando o estado confirmado vale.
	Since time.Time
}

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
