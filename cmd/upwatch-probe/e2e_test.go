package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A ponta a ponta sobe tudo em contêineres: um Redis, o UpWatch central
// compilado deste repositório e a sonda pela imagem do Dockerfile.probe,
// com as mesmas restrições que o README manda usar. Ela prova o que
// nenhum teste de unidade prova: que o sinal atravessa de verdade, chega
// ao central com a causa certa, e que o sinal de vida para quando a sonda
// para.
//
// Precisa de Docker e leva cerca de um minuto, então só roda com
// UPWATCH_TEST_PROBE_E2E=1. Sem a variável o teste se anuncia como
// pulado.

const alpineImage = "alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a"

// e2eSetupToken é o token de configuração inicial do central da ponta a
// ponta, fixado pela variável para o teste não precisar ler o log.
const e2eSetupToken = "token-de-configuracao-do-e2e"

type e2e struct {
	t       *testing.T
	id      string
	network string
	api     string
	client  *http.Client
}

func (e *e2e) name(role string) string { return "upw-e2e-" + e.id + "-" + role }

func (e *e2e) docker(args ...string) string {
	e.t.Helper()
	out, err := e.tryDocker(args...)
	if err != nil {
		e.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (e *e2e) tryDocker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	// O Git Bash do Windows reescreve "/config" como caminho do Windows;
	// esta variável o desliga para o docker receber o que foi escrito.
	cmd.Env = append(os.Environ(), "MSYS_NO_PATHCONV=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func (e *e2e) request(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.api+path, r)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (e *e2e) eventually(what string, timeout time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(time.Second)
	}
}

type beat struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// lastBeat devolve a batida mais recente do monitor no central.
func (e *e2e) lastBeat(monitorID int64) (beat, bool) {
	code, raw := e.request(http.MethodGet, fmt.Sprintf("/api/v1/monitors/%d/heartbeats?limit=1", monitorID), nil)
	if code != http.StatusOK {
		return beat{}, false
	}
	var resp struct {
		Items []beat `json:"items"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Items) == 0 {
		return beat{}, false
	}
	return resp.Items[0], true
}

func (e *e2e) createPushMonitor(name, token string) int64 {
	e.t.Helper()
	code, raw := e.request(http.MethodPost, "/api/v1/monitors", map[string]any{
		"name": name, "type": "push", "interval_seconds": 5, "timeout_seconds": 2,
		"confirmation_threshold": 1,
		"config":                 map[string]any{"token": token, "grace_period_seconds": 5},
	})
	if code != http.StatusCreated {
		e.t.Fatalf("create monitor %s = %d %s", name, code, raw)
	}
	var m struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatal(err)
	}
	return m.ID
}

func TestE2EProbeReportsThroughTheCentral(t *testing.T) {
	if os.Getenv("UPWATCH_TEST_PROBE_E2E") == "" {
		t.Skip("ponta a ponta com Docker; defina UPWATCH_TEST_PROBE_E2E=1 para rodar")
	}
	e := &e2e{t: t, id: randomID()}
	e.network = e.name("net")
	root := repoRoot(t)

	// Limpeza de tudo o que foi criado, mesmo quando o teste falha no
	// meio: um contêiner esquecido segura a porta e o nome na próxima
	// rodada.
	t.Cleanup(func() {
		if t.Failed() {
			for _, role := range []string{"central", "probe"} {
				out, _ := e.tryDocker("logs", "--tail", "40", e.name(role))
				t.Logf("logs de %s:\n%s", role, out)
			}
		}
		_, _ = e.tryDocker("rm", "-f", e.name("probe"), e.name("central"), e.name("redis"))
		_, _ = e.tryDocker("network", "rm", e.network)
		_, _ = e.tryDocker("volume", "rm", "-f", e.name("config"))
	})

	// O central, compilado deste repositório para o Linux do Docker.
	arch := e.docker("version", "--format", "{{.Server.Arch}}")
	central := filepath.Join(t.TempDir(), "upwatch")
	build := exec.Command("go", "build", "-trimpath", "-o", central, "./cmd/upwatch")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/upwatch: %v\n%s", err, out)
	}

	// A sonda, pela imagem de verdade.
	image := "upwatch-probe:e2e-" + e.id
	e.docker("build", "-q", "-f", filepath.Join(root, "Dockerfile.probe"), "-t", image, root)
	t.Cleanup(func() { _, _ = e.tryDocker("rmi", "-f", image) })

	e.docker("network", "create", e.network)
	e.docker("run", "-d", "--name", e.name("redis"), "--network", e.network, "--network-alias", "redis", "redis:7-alpine")

	e.docker("create", "--name", e.name("central"), "--network", e.network, "--network-alias", "central",
		"-p", "127.0.0.1::8080",
		"-e", "UPWATCH_LISTEN=0.0.0.0:8080", "-e", "UPWATCH_DB_DSN=/tmp/upwatch.db",
		"-e", "UPWATCH_SETUP_TOKEN="+e2eSetupToken,
		alpineImage, "/upwatch")
	e.docker("cp", central, e.name("central")+":/upwatch")
	e.docker("start", e.name("central"))

	port := e.docker("port", e.name("central"), "8080/tcp")
	e.api = "http://" + strings.Split(port, "\n")[0]
	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar, Timeout: 5 * time.Second}

	e.eventually("central no ar", time.Minute, func() bool {
		code, _ := e.request(http.MethodGet, "/healthz", nil)
		return code == http.StatusOK
	})
	creds := map[string]string{"username": "admin", "password": "senha-longa-do-e2e"}
	setup := map[string]string{"username": creds["username"], "password": creds["password"], "setup_token": e2eSetupToken}
	if code, raw := e.request(http.MethodPost, "/api/v1/setup", setup); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	if code, raw := e.request(http.MethodPost, "/api/v1/auth/login", creds); code != http.StatusOK {
		t.Fatalf("login = %d %s", code, raw)
	}

	checkToken, hbToken := "e2e-fila-"+randomID()+randomID(), "e2e-vida-"+randomID()+randomID()
	checkID := e.createPushMonitor("rq-workers", checkToken)
	hbID := e.createPushMonitor("sonda e2e", hbToken)
	httpToken := "e2e-http-" + randomID() + randomID()
	httpID := e.createPushMonitor("central health", httpToken)

	// A sonda trancada como o README manda.
	e.docker("run", "-d", "--name", e.name("probe"), "--network", e.network,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--memory", "64m", "--cpus", "0.2", "--pids-limit", "64",
		"-v", e.name("config")+":/config", image)

	probe := func(args ...string) string {
		return e.docker(append([]string{"exec", e.name("probe"), "upwatch-probe"}, args...)...)
	}
	probe("init", "--name", "e2e", "--allow-http-push",
		"--heartbeat-url", "http://central:8080/api/v1/push/"+hbToken, "--heartbeat-interval", "5s")
	probe("add", "--name", "rq-workers", "--type", "redis", "--target", "redis:6379",
		"--assert", "SCARD rq:workers:default >= 1",
		"--push-url", "http://central:8080/api/v1/push/"+checkToken, "--interval", "5s", "--timeout", "2s")

	probe("add", "--name", "central-health", "--type", "http", "--target", "http://central:8080/healthz",
		"--expect-status", "200",
		"--push-url", "http://central:8080/api/v1/push/"+httpToken, "--interval", "5s", "--timeout", "2s")

	// O arquivo nasce com 600, e com o usuário sem privilégio.
	if perm := e.docker("run", "--rm", "-v", e.name("config")+":/c", alpineImage, "stat", "-c", "%a %u", "/c/probe.yaml"); perm != "600 65532" {
		t.Errorf("probe.yaml mode and owner = %q, want 600 65532", perm)
	}
	dry := probe("test", "rq-workers", "--dry-run")
	if strings.Contains(dry, checkToken) {
		t.Errorf("test --dry-run shows the token:\n%s", dry)
	}
	t.Logf("test --dry-run:\n%s", dry)

	// Sem worker, o central recebe down com a causa do checker.
	const cause = "SCARD rq:workers:default = 0, esperado >= 1"
	e.eventually("central recebe down com a causa do SCARD", 90*time.Second, func() bool {
		b, ok := e.lastBeat(checkID)
		return ok && b.Status == "down" && strings.Contains(b.Message, cause)
	})
	t.Logf("status:\n%s", probe("status"))

	e.eventually("sinal de vida no ar", 60*time.Second, func() bool {
		b, ok := e.lastBeat(hbID)
		return ok && b.Status == "up"
	})
	e.eventually("verificação HTTP no ar", 60*time.Second, func() bool {
		b, ok := e.lastBeat(httpID)
		return ok && b.Status == "up"
	})

	// Com um worker registrado, volta a up.
	e.docker("exec", e.name("redis"), "redis-cli", "SADD", "rq:workers:default", "worker-1")
	e.eventually("central volta a up", 60*time.Second, func() bool {
		b, ok := e.lastBeat(checkID)
		return ok && b.Status == "up"
	})

	// O log do daemon nunca mostra o token de push.
	if logs := e.docker("logs", e.name("probe")); strings.Contains(logs, checkToken) || strings.Contains(logs, hbToken) {
		t.Errorf("probe log shows a push token:\n%s", logs)
	}

	// Parar a sonda silencia o sinal de vida, e o central o derruba.
	e.docker("stop", e.name("probe"))
	e.eventually("sinal de vida cai com a sonda parada", 90*time.Second, func() bool {
		b, ok := e.lastBeat(hbID)
		return ok && b.Status == "down"
	})
	b, _ := e.lastBeat(hbID)
	t.Logf("sinal de vida depois de parar a sonda: %+v", b)
}
