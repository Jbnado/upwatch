package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
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

	conta := mustUser(r)

	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case e, open := <-events:
			if !open {
				return
			}
			// O escopo é relido a cada evento, e não guardado na conexão:
			// uma concessão retirada, ou uma conta apagada, valem para o
			// fluxo já aberto, que pode durar dias numa aba esquecida.
			atual, err := a.store.Users().Get(r.Context(), conta.ID)
			if err != nil {
				return
			}
			v, err := a.resolveViewer(r.Context(), atual)
			if err != nil {
				continue
			}
			e, ok := e.visibleTo(v.access)
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

// subtleEqual compara segredos em tempo constante.
//
// Comparação comum encerra no primeiro byte diferente, e a variação de
// tempo permitiria descobrir um token caractere a caractere.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
