package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
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
			payload, err := json.Marshal(e.Data)
			if err != nil {
				slog.Error("api: falha ao serializar evento", "erro", err, "tipo", e.Type)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, payload)
			flusher.Flush()

		case <-keepAlive.C:
			// Comentário SSE: mantém a conexão viva sem entregar dado.
			fmt.Fprint(w, ": keep-alive\n\n")
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
