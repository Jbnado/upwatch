package notifier_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// Num 307 para outro host, o Go só tira Authorization e Cookie: o
// X-Api-Key do webhook iria junto para quem redirecionou. A entrega não
// segue, e a falha diz por quê.
func TestWebhookDoesNotFollowRedirectsWithItsHeaders(t *testing.T) {
	var chaveVazada atomic.Value
	var hitsOutro atomic.Int32
	outro := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsOutro.Add(1)
		chaveVazada.Store(r.Header.Get("X-Api-Key"))
	}))
	t.Cleanup(outro.Close)

	for _, codigo := range []int{http.StatusMovedPermanently, http.StatusFound,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		origem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, outro.URL+"/coleta", codigo)
		}))
		t.Cleanup(origem.Close)

		w, err := notifier.NewWebhook(json.RawMessage(
			`{"url":"` + origem.URL + `/hook","headers":{"X-Api-Key":"segredo"}}`))
		if err != nil {
			t.Fatalf("NewWebhook returned unexpected error: %v", err)
		}

		err = send(t, w, outage())
		if err == nil {
			t.Fatalf("%d: Send succeeded, want a failed delivery", codigo)
		}
		if !strings.Contains(err.Error(), "redirecionamento") {
			t.Errorf("%d: error = %q, want it to explain the redirect", codigo, err)
		}
		if !notifier.IsPermanent(err) {
			t.Errorf("%d: a redirect is a configuration problem; retrying cannot fix it", codigo)
		}
	}
	if hitsOutro.Load() != 0 {
		t.Errorf("the redirect target received %d requests (X-Api-Key %v), want none",
			hitsOutro.Load(), chaveVazada.Load())
	}
}

// Cabeçalho próprio é credencial do destino; por http, atravessaria a
// rede em claro. A regra vale no cadastro e na edição.
func TestValidateRefusesHeadersOverPlainHTTP(t *testing.T) {
	casos := []struct {
		tipo, config string
		recusa       bool
	}{
		{"webhook", `{"url":"http://automacao.exemplo/h","headers":{"X-Api-Key":"s"}}`, true},
		{"WEBHOOK", `{"url":"HTTP://automacao.exemplo/h","headers":{"X-Api-Key":"s"}}`, true},
		{"discord", `{"url":"http://discord.exemplo/api/webhooks/1/t","headers":{"X-K":"s"}}`, true},
		{"slack", `{"url":"http://slack.exemplo/services/x","headers":{"X-K":"s"}}`, true},
		{"webhook", `{"url":"https://automacao.exemplo/h","headers":{"X-Api-Key":"s"}}`, false},
		{"webhook", `{"url":"http://automacao.exemplo/h"}`, false},
		{"webhook", `{"url":"http://127.0.0.1:9000/h","headers":{"X-Api-Key":"s"}}`, false},
		{"webhook", `{"url":"http://localhost:9000/h","headers":{"X-Api-Key":"s"}}`, false},
		{"webhook", `{"url":"http://[::1]:9000/h","headers":{"X-Api-Key":"s"}}`, false},
	}
	for _, c := range casos {
		err := notifier.Validate(c.tipo, json.RawMessage(c.config))
		if c.recusa && (err == nil || !strings.Contains(err.Error(), "https")) {
			t.Errorf("Validate(%s, %s) = %v, want a refusal asking for https", c.tipo, c.config, err)
		}
		if !c.recusa && err != nil {
			t.Errorf("Validate(%s, %s) = %v, want it accepted", c.tipo, c.config, err)
		}
	}
}

// O canal já gravado com http e cabeçalho segue entregando: a regra vale
// para o que se cadastra daqui em diante, e calar um canal que funcionava
// no meio de um incidente seria pior.
func TestBuildKeepsExistingHeadersOverHTTPWorking(t *testing.T) {
	if _, err := notifier.Build("webhook",
		json.RawMessage(`{"url":"http://automacao.exemplo/h","headers":{"X-Api-Key":"s"}}`)); err != nil {
		t.Fatalf("Build returned unexpected error: %v", err)
	}
}

// Validate também recusa o que o construtor recusa.
func TestValidateIncludesBuild(t *testing.T) {
	if err := notifier.Validate("webhook", json.RawMessage(`{"url":""}`)); err == nil {
		t.Error("Validate accepted a webhook without url")
	}
	if err := notifier.Validate("pombo", json.RawMessage(`{}`)); err == nil {
		t.Error("Validate accepted an unknown channel type")
	}
}
