package probe_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/probe"
)

// O que sai da rede é pouco e previsível: estado, causa curta e latência.
// A causa passa pela mesma limpeza do central antes de sair, para o
// corte de 200 caracteres acontecer aqui e o que chega lá ser exatamente
// o que o dry-run mostrou.
func TestSignalFromResultCarriesOnlyStatusCauseAndLatency(t *testing.T) {
	long := strings.Repeat("á", 300)
	sig := probe.SignalFrom(checker.Result{
		Status:    domain.StatusDown,
		LatencyMS: 42,
		Message:   "SCARD rq:workers:default = 0\nlinha forjada " + long,
		Meta:      map[string]string{"host": "redis", "body": "corpo inteiro da resposta"},
	})

	if sig.Status != "down" || sig.LatencyMS != 42 {
		t.Errorf("signal = %+v", sig)
	}
	if strings.ContainsAny(sig.Msg, "\r\n") {
		t.Errorf("msg keeps a line break: %q", sig.Msg)
	}
	if n := utf8.RuneCountInString(sig.Msg); n > domain.MaxPushMessageLength {
		t.Errorf("msg has %d characters, want at most %d", n, domain.MaxPushMessageLength)
	}

	var body map[string]any
	if err := json.Unmarshal(sig.Body(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	for key := range body {
		if key != "status" && key != "msg" && key != "latency_ms" {
			t.Errorf("body carries %q; only status, msg and latency_ms may leave the network", key)
		}
	}
}

// "unknown" é recusado pelo central. A sonda não sabe dizer se o alvo
// está de pé, e calar faria o monitor cair por silêncio com a causa
// errada; vai como down, dizendo que não houve medição.
func TestSignalFromUnknownIsDownWithTheReason(t *testing.T) {
	sig := probe.SignalFrom(checker.Result{Status: domain.StatusUnknown, Message: "sem rede"})
	if sig.Status != "down" || !strings.Contains(sig.Msg, "sem rede") {
		t.Errorf("signal = %+v", sig)
	}
}

func TestSignalFromClampsLatency(t *testing.T) {
	if got := probe.SignalFrom(checker.Result{Status: domain.StatusUp, LatencyMS: -5}).LatencyMS; got != 0 {
		t.Errorf("negative latency became %d, want 0", got)
	}
	huge := probe.SignalFrom(checker.Result{Status: domain.StatusUp, LatencyMS: domain.MaxPushLatencyMS * 2})
	if huge.LatencyMS != domain.MaxPushLatencyMS {
		t.Errorf("huge latency became %d, want the central's ceiling", huge.LatencyMS)
	}
}

type received struct {
	method, path, contentType, agent string
	body                             pushBody
}

type pushBody struct {
	Status    string `json:"status"`
	Msg       string `json:"msg"`
	LatencyMS int64  `json:"latency_ms"`
}

func pushServer(t *testing.T, status int, header http.Header) (*httptest.Server, chan received) {
	t.Helper()
	got := make(chan received, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b pushBody
		_ = json.Unmarshal(raw, &b)
		got <- received{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), b}
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

const token = "token-da-verificacao-0001"

func TestSenderPostsTheSignalAsJSON(t *testing.T) {
	srv, got := pushServer(t, http.StatusOK, nil)
	s := probe.NewSender("teste")

	sig := probe.Signal{Status: "down", Msg: "SCARD rq:workers:default = 0, esperado >= 1", LatencyMS: 7}
	if err := s.Send(context.Background(), srv.URL+"/api/v1/push/"+token, sig); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	r := <-got
	if r.method != http.MethodPost || r.path != "/api/v1/push/"+token {
		t.Errorf("request = %s %s", r.method, r.path)
	}
	// Sem Content-Type JSON o central ignora o corpo e o sinal chega como
	// "up" sem causa: o pior engano possível.
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q", r.contentType)
	}
	if r.body.Status != "down" || r.body.Msg != sig.Msg || r.body.LatencyMS != 7 {
		t.Errorf("body = %+v, want %+v", r.body, sig)
	}
	if !strings.HasPrefix(r.agent, "upwatch-probe/") {
		t.Errorf("User-Agent = %q", r.agent)
	}
}

// 401 é token errado ou monitor apagado: repetir não muda nada, e só
// gastaria o limite de taxa do central. 5xx e 429 passam.
func TestSenderClassifiesFailures(t *testing.T) {
	cases := []struct {
		status    int
		retryable bool
	}{
		{http.StatusUnauthorized, false},
		{http.StatusBadRequest, false},
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, c := range cases {
		srv, _ := pushServer(t, c.status, http.Header{"Retry-After": {"7"}})
		err := probe.NewSender("teste").Send(context.Background(), srv.URL+"/api/v1/push/"+token, probe.Signal{Status: "up"})

		var se *probe.SendError
		if !errors.As(err, &se) {
			t.Fatalf("status %d: err = %v, want *SendError", c.status, err)
		}
		if se.Retryable != c.retryable || se.HTTPStatus != c.status {
			t.Errorf("status %d: %+v, want retryable=%v", c.status, se, c.retryable)
		}
		if c.status == http.StatusTooManyRequests && se.RetryAfter != 7*time.Second {
			t.Errorf("429 RetryAfter = %s, want 7s from the header", se.RetryAfter)
		}
	}
}

// Seguir um redirecionamento mandaria o token a outro host, e o POST
// viraria GET sem corpo, que o central lê como "up".
func TestSenderDoesNotFollowRedirects(t *testing.T) {
	srv, _ := pushServer(t, http.StatusFound, http.Header{"Location": {"https://outro.exemplo.com/"}})
	err := probe.NewSender("teste").Send(context.Background(), srv.URL+"/api/v1/push/"+token, probe.Signal{Status: "up"})
	var se *probe.SendError
	if !errors.As(err, &se) || se.Retryable {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
}

// O erro do cliente HTTP do Go traz a URL inteira, com o token. Ele vai
// para o log e para o status, que aparecem na tela gravada do jump
// server.
func TestSenderErrorNeverShowsTheToken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	err = probe.NewSender("teste").Send(context.Background(), "http://"+addr+"/api/v1/push/"+token, probe.Signal{Status: "up"})
	if err == nil {
		t.Fatal("Send to a closed port succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error shows the token: %v", err)
	}
	var se *probe.SendError
	if !errors.As(err, &se) || !se.Retryable {
		t.Errorf("connection refused should be retryable, got %+v", err)
	}
}

// O dry-run mostra o corpo a quem confere o que sai da rede; a causa tem
// de aparecer como foi escrita, e não com < e > escapados.
func TestSignalBodyKeepsComparisonsReadable(t *testing.T) {
	body := string(probe.Signal{Status: "down", Msg: "SCARD k = 0, esperado >= 1", LatencyMS: 1}.Body())
	want := `{"status":"down","msg":"SCARD k = 0, esperado >= 1","latency_ms":1}`
	if body != want {
		t.Errorf("Body = %s, want %s", body, want)
	}
}
