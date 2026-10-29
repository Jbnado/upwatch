package probe_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/probe"
)

// A asserção chega como uma linha só porque o terminal do jump server
// não deixa colar JSON com aspas aninhadas: "SCARD rq:workers:default >= 1"
// passa intacto por qualquer terminal, um objeto JSON quase nunca.
func TestParseRedisAssertionReadsTheOneLineForm(t *testing.T) {
	cases := map[string]checker.RedisAssertion{
		"SCARD rq:workers:default >= 1": {Command: "SCARD", Key: "rq:workers:default", Op: "gte", Value: 1},
		"LLEN rq:queue:default < 100":   {Command: "LLEN", Key: "rq:queue:default", Op: "lt", Value: 100},
		"HGET rq:worker:w1 state != 0":  {Command: "HGET", Key: "rq:worker:w1", Field: "state", Op: "ne", Value: 0},
		"INFO memory used_memory <= 1e9": {
			Command: "INFO", Section: "memory", Field: "used_memory", Op: "lte", Value: 1e9,
		},
		"DBSIZE > 0":       {Command: "DBSIZE", Op: "gt", Value: 0},
		"exists trava = 0": {Command: "EXISTS", Key: "trava", Op: "eq", Value: 0},
		// Espaço a mais entre as partes é o que sobra de colar de um
		// documento; não pode virar erro.
		"  ZCARD   rq:wip:default   ==  3 ": {Command: "ZCARD", Key: "rq:wip:default", Op: "eq", Value: 3},
		// O nome do operador da API também vale, para quem copia do JSON.
		"SCARD k gte 1": {Command: "SCARD", Key: "k", Op: "gte", Value: 1},
		// Worker do Bull/BullMQ: prefixo e fila, como nas chaves da fila.
		"WORKERS bull:excel-render >= 1": {Command: "WORKERS", Key: "bull:excel-render", Op: "gte", Value: 1},
	}
	for line, want := range cases {
		got, err := probe.ParseRedisAssertion(line)
		if err != nil {
			t.Errorf("ParseRedisAssertion(%q) returned unexpected error: %v", line, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ParseRedisAssertion(%q) = %+v, want %+v", line, got, want)
		}
	}
}

// A linha é convertida e depois passa pela mesma validação do checker:
// a lista fechada de comandos de leitura não pode ter uma segunda
// porta de entrada pela sonda.
func TestParseRedisAssertionRejectsWhatTheCheckerRejects(t *testing.T) {
	for _, line := range []string{
		"",
		"SCARD",
		"SCARD k >= abc",
		"SCARD k ~ 1",
		"FLUSHALL >= 0",
		"SET k v >= 1",
		"KEYS * > 0",
		"SCARD a b >= 1",
		"DBSIZE k > 0",
		"INFO memory > 0",
		// Sem prefixo não há como montar o nome que o Bull dá à conexão.
		"WORKERS excel-render >= 1",
	} {
		if _, err := probe.ParseRedisAssertion(line); err == nil {
			t.Errorf("ParseRedisAssertion(%q) accepted an invalid line", line)
		}
	}
}

// A volta para texto é o que o list e a interface mostram; tem de ser a
// mesma linha que o add aceita, para quem copia de um lado e cola no
// outro.
func TestFormatRedisAssertionRoundTrips(t *testing.T) {
	for _, line := range []string{
		"SCARD rq:workers:default >= 1",
		"HGET rq:worker:w1 state != 0",
		"INFO memory used_memory <= 1000000",
		"DBSIZE > 0",
		"WORKERS bull:excel-render >= 1",
	} {
		a, err := probe.ParseRedisAssertion(line)
		if err != nil {
			t.Fatalf("ParseRedisAssertion(%q): %v", line, err)
		}
		if got := probe.FormatRedisAssertion(a); got != line {
			t.Errorf("FormatRedisAssertion = %q, want %q", got, line)
		}
	}
}

// Quem digita FLUSHALL precisa ler que o comando é proibido, e não uma
// queixa sobre a quantidade de argumentos.
func TestParseRedisAssertionNamesTheForbiddenCommand(t *testing.T) {
	_, err := probe.ParseRedisAssertion("FLUSHALL > 0")
	if err == nil || !strings.Contains(err.Error(), "FLUSHALL") || !strings.Contains(err.Error(), "não é permitido") {
		t.Errorf("err = %v, want it to name the forbidden command", err)
	}
}
