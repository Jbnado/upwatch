package probe_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/probe"
	"github.com/Jbnado/upwatch/internal/testdir"
)

// Tokens fictícios: o formato é o do UpWatch, o valor não abre nada.
const (
	checkURL     = "https://upwatch.exemplo.com/api/v1/push/token-da-verificacao-0001"
	heartbeatURL = "https://upwatch.exemplo.com/api/v1/push/token-do-sinal-de-vida-01"
)

func redisCheck(name string) probe.Check {
	return probe.Check{
		Name:     name,
		Type:     "redis",
		Target:   "redis:6379",
		Interval: probe.Duration(time.Minute),
		Timeout:  probe.Duration(5 * time.Second),
		PushURL:  checkURL,
		Config: map[string]any{
			"assertions": []any{
				map[string]any{"command": "SCARD", "key": "rq:workers:default", "op": "gte", "value": 1},
			},
		},
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(testdir.New(t), "probe.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// O arquivo é escrito à mão por quem não tem a documentação aberta: a
// duração vem como "60s", e a configuração do checker como YAML comum,
// que vira o mesmo JSON que o central aceitaria.
func TestLoadReadsTheHandWrittenFormat(t *testing.T) {
	path := writeFile(t, `
name: vm-prod-1
heartbeat:
  push_url: `+heartbeatURL+`
  interval: 30s
checks:
  - name: rq-workers
    type: redis
    target: redis:6379
    interval: 1m
    timeout: 5s
    push_url: `+checkURL+`
    config:
      assertions:
        - {command: SCARD, key: "rq:workers:default", op: gte, value: 1}
`)

	cfg, err := probe.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.Name != "vm-prod-1" || time.Duration(cfg.Heartbeat.Interval) != 30*time.Second {
		t.Errorf("probe settings = %+v", cfg)
	}
	if len(cfg.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(cfg.Checks))
	}

	m, err := cfg.Checks[0].Monitor(1)
	if err != nil {
		t.Fatalf("Monitor returned unexpected error: %v", err)
	}
	if m.Type != domain.MonitorRedis || m.Interval != time.Minute || m.Timeout != 5*time.Second {
		t.Errorf("monitor = %+v", m)
	}
	var got struct {
		Assertions []struct {
			Command string  `json:"command"`
			Value   float64 `json:"value"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(m.Config, &got); err != nil {
		t.Fatalf("config is not JSON: %v (%s)", err, m.Config)
	}
	if len(got.Assertions) != 1 || got.Assertions[0].Command != "SCARD" || got.Assertions[0].Value != 1 {
		t.Errorf("config = %s", m.Config)
	}
}

// Um campo com erro de digitação ("intervall") seria ignorado em
// silêncio e a verificação rodaria com o padrão, sem ninguém saber.
func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeFile(t, "checks:\n  - name: a\n    intervall: 10s\n")
	if _, err := probe.Load(path); err == nil {
		t.Fatal("Load accepted an unknown field")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeFile(t, `
heartbeat:
  push_url: `+heartbeatURL+`
checks:
  - name: bff
    type: http
    target: http://bff-api:3000/health
    push_url: `+checkURL+`
`)
	cfg, err := probe.Load(path)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	c := cfg.Checks[0]
	if time.Duration(c.Interval) != 60*time.Second || time.Duration(c.Timeout) != 10*time.Second {
		t.Errorf("defaults = interval %s, timeout %s", c.Interval, c.Timeout)
	}
	if time.Duration(cfg.Heartbeat.Interval) != 60*time.Second {
		t.Errorf("heartbeat interval = %s, want 60s", cfg.Heartbeat.Interval)
	}
}

func TestLoadReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := probe.Load(filepath.Join(testdir.New(t), "nao-existe.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// O arquivo guarda o token de cada push e, às vezes, senha de Redis ou de
// banco. Legível por outros usuários da máquina, vaza para quem não
// deveria; a sonda recusa como o ssh recusa uma chave aberta.
func TestLoadRejectsAFileOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissão Unix não se aplica ao Windows")
	}
	path := writeFile(t, "checks: []\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := probe.Load(path)
	if err == nil || !strings.Contains(err.Error(), "600") {
		t.Fatalf("Load = %v, want an error asking for mode 600", err)
	}
}

func TestSaveWritesOwnerOnlyAndRoundTrips(t *testing.T) {
	path := filepath.Join(testdir.New(t), "probe.yaml")
	cfg := probe.Config{
		Name:      "vm-prod-1",
		Heartbeat: probe.Heartbeat{PushURL: heartbeatURL, Interval: probe.Duration(time.Minute)},
		Checks:    []probe.Check{redisCheck("rq-workers")},
	}
	if err := probe.Save(path, cfg); err != nil {
		t.Fatalf("Save returned unexpected error: %v", err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 600", perm)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Duração em nanossegundos seria ilegível para quem abre o arquivo.
	if !strings.Contains(string(raw), "interval: 1m0s") {
		t.Errorf("durations are not written as text:\n%s", raw)
	}

	back, err := probe.Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if back.Name != cfg.Name || len(back.Checks) != 1 || back.Checks[0].Target != "redis:6379" {
		t.Errorf("round trip = %+v", back)
	}
}

func TestValidateRejectsBrokenChecks(t *testing.T) {
	cases := map[string]func(*probe.Check){
		"nome vazio":           func(c *probe.Check) { c.Name = "" },
		"nome com espaço":      func(c *probe.Check) { c.Name = "rq workers" },
		"tipo desconhecido":    func(c *probe.Check) { c.Type = "ftp" },
		"push dentro da sonda": func(c *probe.Check) { c.Type = "push" },
		"alvo vazio":           func(c *probe.Check) { c.Target = "" },
		"timeout >= intervalo": func(c *probe.Check) { c.Timeout = c.Interval },
		"intervalo curto":      func(c *probe.Check) { c.Interval = probe.Duration(time.Second) },
		"asserção de escrita": func(c *probe.Check) {
			c.Config = map[string]any{"assertions": []any{
				map[string]any{"command": "FLUSHALL", "op": "eq", "value": 0},
			}}
		},
		"sem push":       func(c *probe.Check) { c.PushURL = "" },
		"push sem token": func(c *probe.Check) { c.PushURL = "https://upwatch.exemplo.com/api/v1/push/" },
		"token curto":    func(c *probe.Check) { c.PushURL = "https://upwatch.exemplo.com/api/v1/push/curto" },
		"push sem https": func(c *probe.Check) { c.PushURL = strings.Replace(checkURL, "https", "http", 1) },
		"push com usuário": func(c *probe.Check) {
			c.PushURL = "https://eu:x@upwatch.exemplo.com/api/v1/push/token-da-verificacao-0001"
		},
		"push com query": func(c *probe.Check) { c.PushURL = checkURL + "?status=up" },
		"push fora do caminho": func(c *probe.Check) {
			c.PushURL = "https://upwatch.exemplo.com/api/v1/monitors/token-da-verificacao-0001"
		},
	}
	for name, mutate := range cases {
		c := redisCheck("rq-workers")
		mutate(&c)
		cfg := probe.Config{Checks: []probe.Check{c}}
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
	}
}

// O token do push viaja no caminho. Em HTTP sem TLS, qualquer um no
// caminho entre a VM e a VPS o lê e passa a poder manter o monitor verde
// com o serviço caído. Só quem pediu explicitamente (o central na mesma
// rede, num teste) manda em claro.
func TestValidateAcceptsPlainHTTPOnlyWhenAllowed(t *testing.T) {
	c := redisCheck("rq-workers")
	c.PushURL = strings.Replace(checkURL, "https", "http", 1)

	cfg := probe.Config{Checks: []probe.Check{c}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted a plain HTTP push without allow_http_push")
	}
	cfg.AllowHTTPPush = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate with allow_http_push returned unexpected error: %v", err)
	}
}

func TestValidateRejectsDuplicateNamesAndBadHeartbeat(t *testing.T) {
	dup := probe.Config{Checks: []probe.Check{redisCheck("a"), redisCheck("a")}}
	if err := dup.Validate(); err == nil {
		t.Error("Validate accepted two checks with the same name")
	}

	hb := probe.Config{Heartbeat: probe.Heartbeat{PushURL: "https://upwatch.exemplo.com/x"}}
	if err := hb.Validate(); err == nil {
		t.Error("Validate accepted a heartbeat URL without a push token")
	}
}

// A tela do jump server costuma ser gravada. O token não pode aparecer
// inteiro em nenhuma saída da sonda — list, status, log ou dry-run.
func TestMaskURLHidesTheToken(t *testing.T) {
	got := probe.MaskURL(checkURL)
	if strings.Contains(got, "token-da-verificacao-0001") {
		t.Fatalf("MaskURL(%q) = %q, still shows the token", checkURL, got)
	}
	if !strings.HasPrefix(got, "https://upwatch.exemplo.com/api/v1/push/toke") {
		t.Errorf("MaskURL = %q, want host and the token prefix kept for identification", got)
	}
	if got := probe.MaskURL("::nao é url"); strings.Contains(got, "nao") {
		t.Errorf("MaskURL of garbage = %q, want it fully hidden", got)
	}
}
