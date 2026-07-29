package checker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/checker"
	"github.com/bernardojoao/upwatch/internal/domain"
)

// httpMonitor monta um monitor HTTP apontando para url, com a config dada.
func httpMonitor(t *testing.T, url string, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage("{}")
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}
	return domain.Monitor{
		ID: 1, Name: "alvo", Type: domain.MonitorHTTP, Target: url,
		Interval: time.Minute, Timeout: 3 * time.Second,
		ConfirmationThreshold: 1, Config: raw,
	}
}

func check(t *testing.T, url string, cfg map[string]any) checker.Result {
	t.Helper()
	return checker.NewHTTP().Check(context.Background(), httpMonitor(t, url, cfg))
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func TestHTTPTypeIsHTTP(t *testing.T) {
	if got := checker.NewHTTP().Type(); got != domain.MonitorHTTP {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorHTTP)
	}
}

func TestHTTPSuccessfulResponseIsUp(t *testing.T) {
	srv := serve(t, status(http.StatusOK))

	got := check(t, srv.URL, nil)

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestHTTPStatusOutsideExpectedRangeIsDown(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := serve(t, status(code))

			got := check(t, srv.URL, nil)

			if got.Status != domain.StatusDown {
				t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
			}
			if !strings.Contains(got.Message, fmt.Sprint(code)) {
				t.Errorf("Message = %q, want it to mention status %d", got.Message, code)
			}
		})
	}
}

func TestHTTPAcceptsAnySuccessStatusByDefault(t *testing.T) {
	for _, code := range []int{200, 201, 204, 299} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := serve(t, status(code))

			if got := check(t, srv.URL, nil); got.Status != domain.StatusUp {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
			}
		})
	}
}

// Endpoint protegido que responde 401 está saudável; o esperado é
// configurável justamente para esses casos.
func TestHTTPHonoursExplicitExpectedStatus(t *testing.T) {
	srv := serve(t, status(http.StatusUnauthorized))

	got := check(t, srv.URL, map[string]any{"expect_status": []int{401}})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestHTTPExplicitExpectedStatusRejectsOthers(t *testing.T) {
	srv := serve(t, status(http.StatusOK))

	got := check(t, srv.URL, map[string]any{"expect_status": []int{401}})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestHTTPRecordsStatusCodeInMeta(t *testing.T) {
	srv := serve(t, status(http.StatusCreated))

	got := check(t, srv.URL, nil)

	if got.Meta["status_code"] != "201" {
		t.Errorf("Meta[status_code] = %q, want %q", got.Meta["status_code"], "201")
	}
}

func TestHTTPRecordsLatency(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	got := check(t, srv.URL, nil)

	if got.LatencyMS <= 0 {
		t.Errorf("LatencyMS = %d, want a positive measurement", got.LatencyMS)
	}
}

func TestHTTPFollowsRedirectsByDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/final", status(http.StatusOK))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
	})

	got := check(t, srv.URL+"/start", nil)

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Com redirecionamento desligado o 302 é a resposta final e não está na
// faixa esperada — é assim que se detecta redirect indevido.
func TestHTTPWithoutFollowTreatsRedirectAsFinalStatus(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/somewhere", http.StatusFound)
	})

	got := check(t, srv.URL, map[string]any{"follow_redirects": false})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if got.Meta["status_code"] != "302" {
		t.Errorf("Meta[status_code] = %q, want %q", got.Meta["status_code"], "302")
	}
}

func TestHTTPRedirectLoopIsDown(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	})

	got := check(t, srv.URL, nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestHTTPTimeoutIsDown(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	m := httpMonitor(t, srv.URL, nil)
	m.Timeout = 50 * time.Millisecond
	got := checker.NewHTTP().Check(context.Background(), m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if got.Message == "" {
		t.Error("Message is empty, want an explanation of the timeout")
	}
}

func TestHTTPConnectionRefusedIsDown(t *testing.T) {
	srv := httptest.NewServer(status(http.StatusOK))
	url := srv.URL
	srv.Close() // porta fechada a partir daqui

	got := check(t, url, nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestHTTPUnresolvableHostIsDown(t *testing.T) {
	got := check(t, "http://nao-existe.invalid/health", nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Certificado autoassinado precisa reprovar por padrão: aceitar em
// silêncio tornaria o monitor cego a certificado trocado.
func TestHTTPUntrustedCertificateIsDown(t *testing.T) {
	srv := httptest.NewTLSServer(status(http.StatusOK))
	t.Cleanup(srv.Close)

	got := check(t, srv.URL, nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestHTTPSkipTLSVerifyAcceptsSelfSignedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(status(http.StatusOK))
	t.Cleanup(srv.Close)

	got := check(t, srv.URL, map[string]any{"skip_tls_verify": true})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestHTTPBodyContains(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"healthy","version":"1.2.3"}`)
	})

	tests := []struct {
		name   string
		needle string
		want   domain.Status
	}{
		{"presente", "healthy", domain.StatusUp},
		{"ausente", "degraded", domain.StatusDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check(t, srv.URL, map[string]any{"body_contains": tt.needle})
			if got.Status != tt.want {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, tt.want)
			}
		})
	}
}

func TestHTTPBodyNotContains(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "service temporarily unavailable")
	})

	tests := []struct {
		name   string
		needle string
		want   domain.Status
	}{
		{"termo proibido presente", "unavailable", domain.StatusDown},
		{"termo proibido ausente", "panic", domain.StatusUp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check(t, srv.URL, map[string]any{"body_not_contains": tt.needle})
			if got.Status != tt.want {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, tt.want)
			}
		})
	}
}

func TestHTTPBodyRegex(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"uptime_seconds": 98765}`)
	})

	tests := []struct {
		name    string
		pattern string
		want    domain.Status
	}{
		{"casa", `"uptime_seconds":\s*\d+`, domain.StatusUp},
		{"não casa", `"errors":\s*\d+`, domain.StatusDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check(t, srv.URL, map[string]any{"body_regex": tt.pattern})
			if got.Status != tt.want {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, tt.want)
			}
		})
	}
}

func TestHTTPSendsCustomHeaders(t *testing.T) {
	var got http.Header
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})

	check(t, srv.URL, map[string]any{
		"headers": map[string]string{"X-Api-Key": "segredo", "Accept": "application/json"},
	})

	if got.Get("X-Api-Key") != "segredo" {
		t.Errorf("X-Api-Key = %q, want %q", got.Get("X-Api-Key"), "segredo")
	}
	if got.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q, want %q", got.Get("Accept"), "application/json")
	}
}

// Sem User-Agent próprio o alvo não consegue identificar quem o sonda.
func TestHTTPSendsIdentifyingUserAgent(t *testing.T) {
	var ua string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		w.WriteHeader(http.StatusOK)
	})

	check(t, srv.URL, nil)

	if !strings.Contains(strings.ToLower(ua), "upwatch") {
		t.Errorf("User-Agent = %q, want it to identify UpWatch", ua)
	}
}

func TestHTTPSendsBasicAuth(t *testing.T) {
	var (
		user, pass string
		ok         bool
	)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	})

	check(t, srv.URL, map[string]any{"basic_auth_user": "admin", "basic_auth_pass": "s3nha"})

	if !ok {
		t.Fatal("request carried no basic auth credentials")
	}
	if user != "admin" || pass != "s3nha" {
		t.Errorf("credentials = (%q, %q), want (admin, s3nha)", user, pass)
	}
}

func TestHTTPUsesGetByDefault(t *testing.T) {
	var method string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusOK)
	})

	check(t, srv.URL, nil)

	if method != http.MethodGet {
		t.Errorf("method = %q, want %q", method, http.MethodGet)
	}
}

func TestHTTPSendsConfiguredMethodAndBody(t *testing.T) {
	var (
		method string
		body   []byte
	)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})

	check(t, srv.URL, map[string]any{"method": "POST", "body": `{"ping":true}`})

	if method != http.MethodPost {
		t.Errorf("method = %q, want %q", method, http.MethodPost)
	}
	if string(body) != `{"ping":true}` {
		t.Errorf("body = %q, want %q", body, `{"ping":true}`)
	}
}

// Um alvo que responde gigabytes não pode consumir a memória do monitor.
// A leitura é limitada, então conteúdo além do teto não é considerado.
func TestHTTPLimitsBodyRead(t *testing.T) {
	const marker = "MARCADOR_ALEM_DO_LIMITE"
	srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		filler := strings.Repeat("a", checker.MaxBodyBytes+1024)
		fmt.Fprint(w, filler+marker)
	})

	got := check(t, srv.URL, map[string]any{"body_contains": marker})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v: content past the read limit must not match", got.Status, domain.StatusDown)
	}
}

func TestHTTPRespectsCancelledContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	got := checker.NewHTTP().Check(ctx, httpMonitor(t, srv.URL, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestHTTPInvalidTargetIsDown(t *testing.T) {
	got := check(t, "não é uma url://", nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// ---------- validação de configuração ----------

func TestHTTPValidateConfigAcceptsEmptyConfig(t *testing.T) {
	if err := checker.NewHTTP().ValidateConfig(json.RawMessage("{}")); err != nil {
		t.Errorf("ValidateConfig({}) returned unexpected error: %v", err)
	}
}

func TestHTTPValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := checker.NewHTTP().ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

// Regex inválida precisa reprovar no cadastro; descobrir só na primeira
// execução deixaria o monitor quebrado sem aviso.
func TestHTTPValidateConfigRejectsInvalidRegex(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"body_regex": "([desbalanceado"})

	if err := checker.NewHTTP().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with an invalid regex returned nil error, want an error")
	}
}

func TestHTTPValidateConfigRejectsUnknownMethod(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"method": "TELEPORT"})

	if err := checker.NewHTTP().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with an unknown method returned nil error, want an error")
	}
}

func TestHTTPValidateConfigRejectsOutOfRangeStatus(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"expect_status": []int{99}})

	if err := checker.NewHTTP().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with an out-of-range status returned nil error, want an error")
	}
}
