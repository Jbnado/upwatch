package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/probe"
)

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// O assistente é o caminho de quem chega no terminal web do jump server
// sem a documentação aberta: uma pergunta por linha, Enter aceita o que
// está entre colchetes, e a resposta errada é recusada na hora, com o
// motivo, sem perder as anteriores.
func TestSetupConfiguresRedisAndHTTPLineByLine(t *testing.T) {
	h := newHarness(t)
	var tested []string
	h.measure = func(_ context.Context, c probe.Check) probe.Signal {
		tested = append(tested, c.Name)
		return probe.Signal{Status: "up", LatencyMS: 2}
	}
	h.stdin = lines(
		"vm prod",        // nome inválido: tem espaço
		"vm-prod-1",      // nome
		pushBase+"curto", // sinal de vida: token curto demais
		pushBase+hbToken, // sinal de vida
		"",               // intervalo do sinal de vida [60s]
		"",               // adicionar verificação? [s]
		"rq-workers",     // nome
		"ftp",            // tipo inválido
		"redis",          // tipo
		"redis:6379",     // alvo
		"FLUSHALL > 0",   // asserção recusada: escrita
		redisLine,        // asserção
		"",               // fim das asserções
		"",               // intervalo [60s]
		pushBase+token,   // push
		"",               // salvar? [s]
		"s",              // outra verificação?
		"bff",            // nome
		"",               // tipo [http]
		"http://bff-api:3000/health",
		"",             // status esperado [qualquer 2xx]
		"30s",          // intervalo
		pushBase+token, // push
		"",             // salvar? [s]
		"",             // outra verificação? [n]
	)

	out := h.mustRun("setup")

	for _, want := range []string{"[60s]", "nome", "FLUSHALL"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Errorf("setup output misses %q:\n%s", want, out)
		}
	}
	cfg := h.config()
	if cfg.Name != "vm-prod-1" || cfg.Heartbeat.PushURL != pushBase+hbToken {
		t.Errorf("probe = %+v", cfg)
	}
	if len(cfg.Checks) != 2 {
		t.Fatalf("got %d checks, want 2:\n%s", len(cfg.Checks), out)
	}
	if got := probe.Describe(cfg.Checks[0]); got != redisLine {
		t.Errorf("redis check = %q", got)
	}
	if bff, _ := cfg.Find("bff"); bff == nil || time.Duration(bff.Interval) != 30*time.Second {
		t.Errorf("bff = %+v", bff)
	}
	// Cada verificação é testada antes de ser gravada.
	if strings.Join(tested, ",") != "rq-workers,bff" {
		t.Errorf("tested %v before saving, want both", tested)
	}
	// O token da interface sai na tela uma vez, ao fim do setup.
	if cfg.UIToken == "" || !strings.Contains(out, cfg.UIToken) {
		t.Errorf("setup did not generate and show the UI token")
	}
	if strings.Contains(out, token) || strings.Contains(out, hbToken) {
		t.Errorf("setup echoes a push token:\n%s", out)
	}
}

// Uma verificação que falha no teste não é gravada sem confirmação: o
// padrão passa a ser "não", porque gravar alvo errado gera aviso falso
// no central a cada minuto.
func TestSetupAsksBeforeSavingAFailingCheck(t *testing.T) {
	h := newHarness(t)
	h.measure = func(context.Context, probe.Check) probe.Signal {
		return probe.Signal{Status: "down", Msg: "conexão recusada"}
	}
	h.stdin = lines(
		"vm", "", // nome, sem sinal de vida
		"", // adicionar? [s]
		"fila", "tcp", "redis:1", "", pushBase+token,
		"", // salvar mesmo assim? [n]
		"", // outra? [n]
	)
	out := h.mustRun("setup")
	if !strings.Contains(out, "conexão recusada") || !strings.Contains(out, "[n]") {
		t.Errorf("setup did not show the failure and ask with default no:\n%s", out)
	}
	if len(h.config().Checks) != 0 {
		t.Error("a failing check was saved without confirmation")
	}
}

// Entrada encerrada no meio (Ctrl-D, sessão caída) não grava pela
// metade.
func TestSetupAbortsOnEndOfInput(t *testing.T) {
	h := newHarness(t)
	h.stdin = "vm\n"
	if code := h.run("setup"); code == 0 {
		t.Fatal("setup succeeded with truncated input")
	}
	if _, err := probe.Load(h.path); err == nil {
		t.Error("truncated setup wrote a config")
	}
}
