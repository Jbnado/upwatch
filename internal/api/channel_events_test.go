package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// O conjunto de eventos que um canal assina entra pela API e volta na
// leitura, para a tela mostrar o que está silenciado. Vazio recebe tudo.
func TestChannelEventsRoundTripThroughAPI(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "só queda e degradação", "type": "webhook",
		"config": map[string]any{"url": "https://exemplo.invalido/hook"},
		"events": []string{"monitor.down", "monitor.degraded"},
	})
	criado := readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d, want 201: %s", resp.StatusCode, criado)
	}
	if !strings.Contains(criado, "monitor.down") || !strings.Contains(criado, "monitor.degraded") {
		t.Errorf("a resposta de criação não traz os eventos assinados: %s", criado)
	}
	if strings.Contains(criado, "monitor.up") {
		t.Errorf("a resposta traz um evento que não foi assinado: %s", criado)
	}

	var id int64
	if _, err := fmt.Sscanf(criado[strings.Index(criado, `"id":`)+5:], "%d", &id); err != nil {
		t.Fatalf("no id in the create response: %s", criado)
	}

	r := s.do(t, http.MethodGet, fmt.Sprintf("/api/v1/channels/%d", id), nil)
	corpo := readBody(t, r)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET returned %d: %s", r.StatusCode, corpo)
	}
	if !strings.Contains(corpo, "monitor.down") || !strings.Contains(corpo, "monitor.degraded") {
		t.Errorf("o GET do canal não traz os eventos assinados: %s", corpo)
	}
}

// Filtro que esconde a queda é recusado no cadastro: a tela não pode
// permitir um canal de monitor que cale justamente a queda.
func TestChannelEventsRejectsFilterHidingDown(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "sem queda", "type": "webhook",
		"config": map[string]any{"url": "https://exemplo.invalido/hook"},
		"events": []string{"monitor.up"},
	})
	corpo := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create returned %d, want 400 para filtro sem a queda: %s", resp.StatusCode, corpo)
	}
}
