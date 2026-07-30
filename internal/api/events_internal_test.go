package api

import (
	"sync"
	"testing"
)

// O distribuidor de eventos ao vivo.
//
// O que importa aqui é a limpeza. Cada aba aberta na interface vira um
// assinante, e um assinante que não sai do mapa ao desconectar é
// vazamento de memória num processo que precisa ficar meses no ar — e
// que, pior, continuaria tentando publicar num canal que ninguém lê.

func TestEventHubRemovesSubscriberOnCancel(t *testing.T) {
	h := newEventHub()

	_, cancelar := h.subscribe()
	if got := h.subscriberCount(); got != 1 {
		t.Fatalf("depois de assinar: %d assinantes, esperava 1", got)
	}

	cancelar()
	if got := h.subscriberCount(); got != 0 {
		t.Fatalf("depois de cancelar: %d assinantes, esperava 0", got)
	}
}

func TestEventHubCancelIsIdempotent(t *testing.T) {
	// O handler cancela no defer e a desconexão do cliente pode cancelar
	// antes; a segunda chamada não pode fechar um canal já fechado.
	h := newEventHub()

	_, cancelar := h.subscribe()
	cancelar()
	cancelar()

	if got := h.subscriberCount(); got != 0 {
		t.Fatalf("%d assinantes após cancelar duas vezes", got)
	}
}

func TestEventHubDeliversToEverySubscriber(t *testing.T) {
	h := newEventHub()

	primeiro, cancelaPrimeiro := h.subscribe()
	segundo, cancelaSegundo := h.subscribe()
	defer cancelaPrimeiro()
	defer cancelaSegundo()

	h.publish(event{Type: "monitor.created"})

	for i, ch := range []<-chan event{primeiro, segundo} {
		select {
		case e := <-ch:
			if e.Type != "monitor.created" {
				t.Errorf("assinante %d recebeu %q", i, e.Type)
			}
		default:
			t.Errorf("assinante %d não recebeu o evento", i)
		}
	}
}

func TestEventHubDoesNotBlockOnSlowSubscriber(t *testing.T) {
	// Publicar acontece no caminho das batidas. Uma aba esquecida aberta
	// não pode segurar o registro das medições — o assinante lento perde
	// o evento e a interface se ressincroniza na próxima consulta.
	h := newEventHub()

	_, cancelar := h.subscribe()
	defer cancelar()

	pronto := make(chan struct{})
	go func() {
		defer close(pronto)
		for i := 0; i < eventBuffer*3; i++ {
			h.publish(event{Type: "monitor.updated"})
		}
	}()

	select {
	case <-pronto:
	default:
		// Sem canal cheio ainda; espera a goroutine terminar.
		<-pronto
	}
}

func TestEventHubIsSafeUnderConcurrency(t *testing.T) {
	// Assinantes entram e saem enquanto o agendador publica.
	h := newEventHub()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, cancelar := h.subscribe()
			cancelar()
		}()
		go func() {
			defer wg.Done()
			h.publish(event{Type: "monitor.deleted"})
		}()
	}
	wg.Wait()

	if got := h.subscriberCount(); got != 0 {
		t.Fatalf("%d assinantes sobraram", got)
	}
}
