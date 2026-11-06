package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// eventBuffer é quantos eventos um assinante lento acumula antes de
// começar a perder.
//
// Perder evento é preferível a bloquear: um navegador travado numa aba
// esquecida não pode segurar quem publica, que é o caminho crítico do
// monitoramento.
const eventBuffer = 32

// keepAliveInterval evita que intermediários encerrem a conexão ociosa.
const keepAliveInterval = 25 * time.Second

// eventsRecheckInterval é de quanto em quanto tempo o fluxo reconfere a
// credencial. O fluxo dura dias numa aba esquecida; sem reconferir, uma
// sessão encerrada ou um token revogado continuavam recebendo eventos
// até alguém fechar a aba.
const eventsRecheckInterval = 60 * time.Second

// scopeCacheTTL é por quanto tempo o escopo resolvido serve aos eventos
// seguintes. Reler a conta e a árvore de grupos a cada evento custava
// duas consultas por evento por aba — justamente no momento em que muita
// coisa muda junto. Alguns segundos de atraso para uma concessão
// retirada valer no fluxo aberto é o preço, e é pequeno.
const scopeCacheTTL = 5 * time.Second

// event é uma notificação enviada à interface.
type event struct {
	Type string `json:"type"`
	Data any    `json:"data"`

	// monitor e previous decidem quem recebe o evento: o monitor como
	// ficou e como era antes. Evento sem nenhum dos dois só chega a quem
	// alcança tudo — quem publica e esquece de dizer de qual monitor se
	// trata não pode vazar para todo mundo.
	monitor  *domain.Monitor
	previous *domain.Monitor
	// parent é o pai do monitor, quando há um e foi lido: decide se o
	// parent_id vai para a conta (ver parentVisibility). Sem ele, o id
	// não vai para quem tem escopo.
	parent *domain.Monitor
}

// visibleTo devolve o que a conta pode receber deste evento.
//
// Quem via o monitor e deixou de ver — porque ele mudou para um grupo de
// fora, ou foi apagado — recebe a saída dele, e só ela: o destino do
// monitor movido é justamente o que não se pode contar.
func (e event) visibleTo(acc domain.Access) (event, bool) {
	if e.monitor == nil && e.previous == nil {
		return e, acc.All()
	}
	if e.monitor != nil && acc.Monitor(*e.monitor) {
		if resp, ok := e.Data.(monitorResponse); ok && resp.ParentID != nil &&
			!acc.All() && (e.parent == nil || !acc.Monitor(*e.parent)) {
			resp.ParentID = nil
			e.Data = resp
		}
		return e, true
	}
	if e.previous != nil && acc.Monitor(*e.previous) {
		return event{Type: "monitor.deleted", Data: map[string]int64{"id": e.previous.ID}}, true
	}
	return event{}, false
}

// eventHub distribui eventos aos assinantes conectados.
type eventHub struct {
	mu          sync.Mutex
	subscribers map[chan event]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subscribers: make(map[chan event]struct{})}
}

// subscribe registra um assinante e devolve como cancelá-lo.
func (h *eventHub) subscribe() (<-chan event, func()) {
	ch := make(chan event, eventBuffer)

	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subscribers[ch]; ok {
			delete(h.subscribers, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// publish entrega o evento a quem estiver ouvindo, sem bloquear.
func (h *eventHub) publish(e event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subscribers {
		select {
		case ch <- e:
		default:
			// Assinante lento perde este evento. A interface se
			// ressincroniza na próxima consulta; travar quem publica
			// atrasaria o registro das batidas.
		}
	}
}

// subscriberCount é usado em teste para confirmar a limpeza.
func (h *eventHub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// handleEvents mantém um fluxo de eventos aberto para a interface.
//
// Eventos enviados pelo servidor em vez de WebSocket: o tráfego aqui é de
// mão única, e SSE atravessa proxies reversos e reconecta sozinho sem
// código extra no cliente.
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, codeInternal,
			"este servidor não suporta streaming")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Desliga o buffer de proxies reversos comuns, que de outro modo
	// segurariam os eventos até acumular corpo suficiente.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, unsubscribe := a.events.subscribe()
	defer unsubscribe()

	ctx := r.Context()
	conta := mustUser(r)
	visao, resolvidaEm := viewerFrom(r), a.clock.Now()

	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()

	reconferir := a.clock.NewTimer(eventsRecheckInterval)
	defer reconferir.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-reconferir.C():
			// A credencial da requisição, de novo: sessão encerrada, token
			// revogado ou conta apagada fecham o fluxo. Erro de banco não
			// fecha — derrubar todas as abas num soluço do banco faria
			// todas reconectarem juntas.
			u, err := a.resolveUser(r)
			if errors.Is(err, auth.ErrUnauthenticated) {
				return
			}
			if err == nil {
				if v, err := a.resolveViewer(ctx, u); err == nil {
					visao, resolvidaEm = v, a.clock.Now()
				}
			}
			reconferir.Reset(eventsRecheckInterval)

		case e, open := <-events:
			if !open {
				return
			}
			// O escopo não fica guardado para sempre: uma concessão
			// retirada, ou uma conta apagada, valem para o fluxo aberto
			// depois de scopeCacheTTL.
			if a.clock.Now().Sub(resolvidaEm) >= scopeCacheTTL {
				atual, err := a.store.Users().Get(ctx, conta.ID)
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				if err == nil {
					if v, err := a.resolveViewer(ctx, atual); err == nil {
						visao, resolvidaEm = v, a.clock.Now()
					}
				}
			}
			e, ok := e.visibleTo(visao.access)
			if !ok {
				continue
			}
			payload, err := json.Marshal(e.Data)
			if err != nil {
				slog.Error("api: falha ao serializar evento", "erro", err, "tipo", e.Type)
				continue
			}
			// Falha na escrita significa cliente desconectado. Encerrar
			// aqui libera o assinante na hora; ignorar deixaria o laço
			// girando até o cancelamento do contexto chegar, publicando
			// num socket que ninguém lê.
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload); err != nil {
				return
			}
			flusher.Flush()

		case <-keepAlive.C:
			// Comentário SSE: mantém a conexão viva sem entregar dado. É
			// também como se descobre que o cliente sumiu sem avisar:
			// numa rede que caiu, o erro aparece nesta escrita.
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeStreamFailure registra falha ocorrida com a resposta já em curso.
//
// O status já foi enviado, então não há como transformá-la em erro HTTP; o
// que resta é deixar rastro no log para a investigação.
func writeStreamFailure(_ http.ResponseWriter, err error) {
	slog.Error("api: falha durante o envio da resposta", "erro", err)
}
