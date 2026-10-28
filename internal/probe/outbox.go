package probe

import (
	"context"
	"errors"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
)

// SendFunc entrega um sinal, uma tentativa por chamada.
type SendFunc func(ctx context.Context, pushURL string, sig Signal) error

// Backoff é a política de novas tentativas de uma entrega.
type Backoff struct {
	Initial time.Duration
	Max     time.Duration
	// Attempts conta a primeira tentativa: 4 são uma e mais três.
	Attempts int
}

// DefaultBackoff cabe dentro de um intervalo de um minuto: 2s, 4s e 8s de
// espera, e a quarta tentativa sai antes do próximo resultado.
var DefaultBackoff = Backoff{Initial: 2 * time.Second, Max: 30 * time.Second, Attempts: 4}

// Delay é a espera antes da tentativa seguinte à de número attempt.
func (b Backoff) Delay(attempt int) time.Duration {
	d := b.Initial
	for i := 1; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	return min(d, b.Max)
}

// Delivery descreve uma tentativa de entrega, para o status.
type Delivery struct {
	At         time.Time
	Signal     Signal
	OK         bool
	Attempts   int
	HTTPStatus int
	Err        string
	// GaveUp diz que a sonda parou de tentar este sinal: falha que não
	// melhora com insistência, ou tentativas esgotadas.
	GaveUp bool
}

// Outbox entrega os sinais de um endereço de push, um de cada vez.
//
// Guarda no máximo um sinal pendente. Um resultado novo substitui o que
// ainda não saiu, mesmo no meio das novas tentativas: só o último
// resultado importa. Uma fila de tudo cresceria sem limite numa queda
// longa do central e, na volta, despejaria nele sinais velhos em
// rajada, cada um contando na confirmação.
type Outbox struct {
	clock   clock.Clock
	url     string
	send    SendFunc
	backoff Backoff
	report  func(Delivery)
	slot    chan Signal
}

// NewOutbox cria a fila de um endereço. report recebe cada tentativa.
func NewOutbox(clk clock.Clock, pushURL string, send SendFunc, b Backoff, report func(Delivery)) *Outbox {
	if report == nil {
		report = func(Delivery) {}
	}
	return &Outbox{clock: clk, url: pushURL, send: send, backoff: b, report: report, slot: make(chan Signal, 1)}
}

// Post agenda o sinal, descartando o que estava pendente.
func (o *Outbox) Post(sig Signal) {
	for {
		select {
		case o.slot <- sig:
			return
		default:
		}
		select {
		case <-o.slot:
		default:
		}
	}
}

// Run entrega os sinais até o contexto acabar.
func (o *Outbox) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sig := <-o.slot:
			o.deliver(ctx, sig)
		}
	}
}

func (o *Outbox) deliver(ctx context.Context, sig Signal) {
	for attempt := 1; ; attempt++ {
		err := o.send(ctx, o.url, sig)
		if ctx.Err() != nil {
			return
		}
		d := Delivery{At: o.clock.Now(), Signal: sig, Attempts: attempt, OK: err == nil}
		if err == nil {
			o.report(d)
			return
		}

		retry, wait := true, o.backoff.Delay(attempt)
		var se *SendError
		if errors.As(err, &se) {
			d.Err, d.HTTPStatus, retry = se.Msg, se.HTTPStatus, se.Retryable
			wait = max(wait, se.RetryAfter)
		} else {
			d.Err = err.Error()
		}
		if !retry || attempt >= o.backoff.Attempts {
			d.GaveUp = true
			o.report(d)
			return
		}
		o.report(d)

		t := o.clock.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case newer := <-o.slot:
			// O resultado novo sai já, com tentativas zeradas: ele é o
			// que vale agora, e o central fora pode ter acabado de voltar.
			t.Stop()
			sig, attempt = newer, 0
		}
	}
}
