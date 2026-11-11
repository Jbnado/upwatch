// Package tokenexpiry avisa o dono de um token de API antes de ele vencer.
//
// Laço à parte do motor de incidentes, como o lembrete de queda: o aviso
// depende do relógio — "faltam 7 dias" —, não de uma batida nova, e o token
// não tem monitor que gere batida. A entrega reusa a mesma fila e os mesmos
// canais dos avisos de monitor; o que muda é o conteúdo (AccountNotice).
package tokenexpiry

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/store"
)

// DefaultTick é de quanto em quanto tempo os tokens a vencer são revistos.
// Os marcos são contados em dias, então rever a cada hora é folgado: o aviso
// sai com, no máximo, essa folga de atraso, irrelevante para uma
// antecedência de dias.
const DefaultTick = time.Hour

// Dispatcher entrega os avisos. A mesma interface estreita do lembrete: o
// verificador não sabe montar a fila, só enfileirar.
type Dispatcher interface {
	Enqueue(n notifier.Notification, channels []notifier.Notifier)
}

// Options configura o verificador.
type Options struct {
	Clock clock.Clock
	Tick  time.Duration
}

// Warner avisa o dono de um token de API quando a validade se aproxima.
type Warner struct {
	store    store.MetadataStore
	dispatch Dispatcher
	opts     Options
}

// NewWarner cria o verificador.
func NewWarner(s store.MetadataStore, d Dispatcher, opts Options) *Warner {
	if opts.Clock == nil {
		opts.Clock = clock.Real()
	}
	if opts.Tick <= 0 {
		opts.Tick = DefaultTick
	}
	return &Warner{store: s, dispatch: d, opts: opts}
}

// Run revê os tokens a vencer até o contexto terminar.
func (w *Warner) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.opts.Clock.After(w.opts.Tick):
			w.Tick(ctx)
		}
	}
}

// Tick emite os avisos de vencimento devidos.
func (w *Warner) Tick(ctx context.Context) {
	if w.dispatch == nil {
		return
	}
	now := w.opts.Clock.Now()

	// A janela cobre o marco mais distante: além dela não há aviso a dar.
	janela := now.Add(time.Duration(slices.Max(domain.TokenExpiryMilestones)) * 24 * time.Hour)
	tokens, err := w.store.Tokens().ListExpiring(ctx, janela)
	if err != nil {
		slog.Error("falha ao listar tokens a vencer", "erro", err)
		return
	}
	if len(tokens) == 0 {
		return
	}

	// Os destinos uma vez, e não por token: todo aviso de conta vai para os
	// mesmos canais. Sem ninguém assinando, não se marca nada — assim o aviso
	// ainda sai quando um canal for criado depois.
	destinos := w.destinations(ctx)
	if len(destinos) == 0 {
		return
	}

	for _, et := range tokens {
		dias, ok := domain.DueTokenExpiryMilestone(now, et.ExpiresAt, et.WarnedDays)
		if !ok {
			continue
		}
		// A marca vem antes do envio, como no lembrete: se dois ticks se
		// cruzarem, só um grava o marco e só um avisa.
		marcou, err := w.store.Tokens().MarkExpiryWarned(ctx, et.TokenID, dias)
		if err != nil {
			slog.Error("falha ao marcar aviso de expiração", "token", et.Name, "erro", err)
			continue
		}
		if !marcou {
			continue
		}
		w.dispatch.Enqueue(notificacao(et, now), destinos)
	}
}

// destinations monta os canais habilitados que assinam o aviso de token
// vencendo. Conjunto de eventos vazio recebe tudo (WantsEvent), então quem
// nunca filtrou os eventos recebe também os de conta.
func (w *Warner) destinations(ctx context.Context) []notifier.Notifier {
	canais, err := w.store.Channels().List(ctx)
	if err != nil {
		slog.Error("falha ao listar canais para aviso de token", "erro", err)
		return nil
	}
	out := make([]notifier.Notifier, 0, len(canais))
	for _, c := range canais {
		if !c.Enabled || !c.WantsEvent(domain.EventAccountTokenExpiring) {
			continue
		}
		n, err := notifier.Build(c.Type, c.Config)
		if err != nil {
			// Canal mal configurado não pode calar os outros.
			slog.Error("canal de aviso inválido", "canal", c.Name, "tipo", c.Type, "erro", err)
			continue
		}
		out = append(out, n)
	}
	return out
}

// notificacao monta o aviso de um token a vencer. DaysLeft é arredondado
// para cima: o dia começado é dia que ainda falta — "vence em 7 dias", não
// "em 6".
func notificacao(et store.ExpiringToken, now time.Time) notifier.Notification {
	dias := int(math.Ceil(et.ExpiresAt.Sub(now).Hours() / 24))
	if dias < 0 {
		dias = 0
	}
	return notifier.Notification{
		Account: &notifier.AccountNotice{
			Event:       domain.EventAccountTokenExpiring,
			Username:    et.Username,
			TokenName:   et.Name,
			TokenPrefix: et.Prefix,
			ExpiresAt:   et.ExpiresAt,
			DaysLeft:    dias,
		},
	}
}
