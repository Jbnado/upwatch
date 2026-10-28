package checker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
)

// A asserção é a parte do checker de Redis que decide; aqui ela é testada
// sem rede, com a resposta já em mãos.

func TestRedisAssertionAcceptsTheClosedListOfReadCommands(t *testing.T) {
	for _, a := range []checker.RedisAssertion{
		{Command: "SCARD", Key: "rq:workers:default", Op: "gte", Value: 1},
		{Command: "LLEN", Key: "rq:queue:default", Op: "lt", Value: 100},
		{Command: "GET", Key: "contador", Op: "eq", Value: 3},
		{Command: "EXISTS", Key: "trava", Op: "eq", Value: 0},
		{Command: "HGET", Key: "rq:worker:w1", Field: "state", Op: "ne", Value: 0},
		{Command: "ZCARD", Key: "rq:wip:default", Op: "lte", Value: 10},
		{Command: "DBSIZE", Op: "gt", Value: 0},
		{Command: "INFO", Section: "memory", Field: "used_memory", Op: "lt", Value: 1 << 30},
		// Minúsculas são o jeito como muita gente digita comando de Redis.
		{Command: "scard", Key: "k", Op: "gte", Value: 1},
	} {
		if err := a.Validate(); err != nil {
			t.Errorf("Validate(%+v) returned unexpected error: %v", a, err)
		}
	}
}

// O monitor guarda credencial de um Redis de produção. Aceitar comando
// livre faria dele um cliente Redis genérico com escrita: quem cadastra
// monitor poderia apagar a base (FLUSHALL), rodar script (EVAL) ou
// reconfigurar o servidor (CONFIG SET) usando a senha que nem consegue ler.
func TestRedisAssertionRejectsAnythingOutsideTheReadList(t *testing.T) {
	for _, cmd := range []string{
		"SET", "DEL", "FLUSHALL", "FLUSHDB", "EVAL", "CONFIG", "SHUTDOWN",
		"KEYS", "SCAN", "SMEMBERS", "LPOP", "SCARD k", "", " SCARD",
	} {
		a := checker.RedisAssertion{Command: cmd, Key: "k", Op: "eq", Value: 1}
		if err := a.Validate(); err == nil {
			t.Errorf("Validate accepted command %q", cmd)
		}
	}
}

func TestRedisAssertionRequiresTheArgumentsOfEachCommand(t *testing.T) {
	for name, a := range map[string]checker.RedisAssertion{
		"SCARD sem chave":       {Command: "SCARD", Op: "eq", Value: 1},
		"HGET sem campo":        {Command: "HGET", Key: "h", Op: "eq", Value: 1},
		"INFO sem seção":        {Command: "INFO", Field: "used_memory", Op: "lt", Value: 1},
		"INFO sem campo":        {Command: "INFO", Section: "memory", Op: "lt", Value: 1},
		"INFO seção estranha":   {Command: "INFO", Section: "memory all", Field: "x", Op: "lt", Value: 1},
		"DBSIZE com chave":      {Command: "DBSIZE", Key: "k", Op: "gt", Value: 0},
		"SCARD com campo":       {Command: "SCARD", Key: "k", Field: "f", Op: "gt", Value: 0},
		"operador desconhecido": {Command: "SCARD", Key: "k", Op: ">=", Value: 1},
		"operador vazio":        {Command: "SCARD", Key: "k", Value: 1},
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, a)
		}
	}
}

// Os argumentos são montados pelo checker, nunca pelo texto digitado: a
// chave vai como um argumento só, mesmo com espaço, e não há como
// acrescentar um segundo comando.
func TestRedisAssertionArgs(t *testing.T) {
	for _, tt := range []struct {
		a    checker.RedisAssertion
		want []any
	}{
		{checker.RedisAssertion{Command: "scard", Key: "rq:workers:default"}, []any{"SCARD", "rq:workers:default"}},
		{checker.RedisAssertion{Command: "GET", Key: "a b; FLUSHALL"}, []any{"GET", "a b; FLUSHALL"}},
		{checker.RedisAssertion{Command: "HGET", Key: "h", Field: "f"}, []any{"HGET", "h", "f"}},
		{checker.RedisAssertion{Command: "DBSIZE"}, []any{"DBSIZE"}},
		{checker.RedisAssertion{Command: "INFO", Section: "memory", Field: "used_memory"}, []any{"INFO", "memory"}},
	} {
		if got := tt.a.Args(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Args(%+v) = %v, want %v", tt.a, got, tt.want)
		}
	}
}

// A causa precisa ser legível por quem recebe o alerta às três da manhã:
// o comando, o valor obtido e o que se esperava, sem precisar abrir o
// cadastro do monitor para entender.
func TestRedisAssertionEvaluateExplainsTheFailure(t *testing.T) {
	a := checker.RedisAssertion{Command: "SCARD", Key: "rq:workers:default", Op: "gte", Value: 1}

	got := a.Evaluate(int64(0))

	if want := "SCARD rq:workers:default = 0, esperado >= 1"; got != want {
		t.Errorf("Evaluate = %q, want %q", got, want)
	}
}

func TestRedisAssertionEvaluatePasses(t *testing.T) {
	for _, tt := range []struct {
		a     checker.RedisAssertion
		reply any
	}{
		{checker.RedisAssertion{Command: "SCARD", Key: "k", Op: "gte", Value: 1}, int64(2)},
		{checker.RedisAssertion{Command: "LLEN", Key: "k", Op: "lt", Value: 100}, int64(99)},
		{checker.RedisAssertion{Command: "LLEN", Key: "k", Op: "lte", Value: 100}, int64(100)},
		{checker.RedisAssertion{Command: "EXISTS", Key: "k", Op: "eq", Value: 1}, int64(1)},
		{checker.RedisAssertion{Command: "EXISTS", Key: "k", Op: "ne", Value: 0}, int64(1)},
		{checker.RedisAssertion{Command: "GET", Key: "k", Op: "gt", Value: 1.5}, "2.25"},
		{checker.RedisAssertion{Command: "HGET", Key: "h", Field: "f", Op: "eq", Value: 7}, "7"},
	} {
		if got := tt.a.Evaluate(tt.reply); got != "" {
			t.Errorf("Evaluate(%+v, %v) = %q, want pass", tt.a, tt.reply, got)
		}
	}
}

func TestRedisAssertionEvaluateFailsEachOperator(t *testing.T) {
	for _, tt := range []struct {
		op    string
		value float64
		reply int64
		want  string
	}{
		{"eq", 1, 2, "LLEN q = 2, esperado == 1"},
		{"ne", 2, 2, "LLEN q = 2, esperado != 2"},
		{"lt", 2, 2, "LLEN q = 2, esperado < 2"},
		{"lte", 1, 2, "LLEN q = 2, esperado <= 1"},
		{"gt", 2, 2, "LLEN q = 2, esperado > 2"},
		{"gte", 3, 2, "LLEN q = 2, esperado >= 3"},
	} {
		a := checker.RedisAssertion{Command: "LLEN", Key: "q", Op: tt.op, Value: tt.value}
		if got := a.Evaluate(tt.reply); got != tt.want {
			t.Errorf("%s: Evaluate = %q, want %q", tt.op, got, tt.want)
		}
	}
}

// GET de chave que não existe não é zero: tratar como zero faria
// "contador >= 0" passar com a chave apagada, escondendo justamente a
// falha que se queria pegar.
func TestRedisAssertionEvaluateMissingKeyFails(t *testing.T) {
	a := checker.RedisAssertion{Command: "GET", Key: "contador", Op: "gte", Value: 0}

	got := a.Evaluate(nil)

	if !strings.Contains(got, "GET contador") || !strings.Contains(got, "não existe") {
		t.Errorf("Evaluate(nil) = %q, want a cause saying the key does not exist", got)
	}
}

// A causa vai para o painel, para o aviso no canal e para o histórico
// exportado. Se ela repetisse o valor lido, uma asserção de GET sobre a
// chave de uma sessão ou de um token viraria um jeito de ler o Redis de
// produção para quem só cadastra monitores. Diz que não é número e o
// tamanho, que bastam para achar o erro de configuração.
func TestRedisAssertionEvaluateNonNumericFails(t *testing.T) {
	for _, a := range []checker.RedisAssertion{
		{Command: "GET", Key: "sessao:42", Op: "eq", Value: 1},
		{Command: "HGET", Key: "usuario:7", Field: "token", Op: "eq", Value: 1},
	} {
		got := a.Evaluate("tok-secreto-do-redis")

		if strings.Contains(got, "tok-secreto") {
			t.Errorf("%s: Evaluate = %q leaks the value read", a.Command, got)
		}
		if !strings.Contains(got, "não é número") || !strings.Contains(got, "20 bytes") {
			t.Errorf("%s: Evaluate = %q, want a cause saying it is not a number and its size", a.Command, got)
		}
	}
}

const infoMemory = "# Memory\r\nused_memory:2048\r\nused_memory_human:2.00K\r\nmaxmemory:0\r\n"

func TestRedisAssertionEvaluateReadsTheInfoField(t *testing.T) {
	a := checker.RedisAssertion{Command: "INFO", Section: "memory", Field: "used_memory", Op: "lte", Value: 1024}

	got := a.Evaluate(infoMemory)

	if want := "INFO memory used_memory = 2048, esperado <= 1024"; got != want {
		t.Errorf("Evaluate = %q, want %q", got, want)
	}
}

// Campo com prefixo igual (used_memory_human) não pode ser confundido com
// o pedido; a leitura casa o nome inteiro.
func TestRedisAssertionEvaluateInfoMatchesTheWholeFieldName(t *testing.T) {
	a := checker.RedisAssertion{Command: "INFO", Section: "memory", Field: "used_memory_human", Op: "gt", Value: 0}

	got := a.Evaluate(infoMemory)

	if !strings.Contains(got, "não é número") {
		t.Errorf("Evaluate = %q, want the non-numeric used_memory_human to fail", got)
	}
}

func TestRedisAssertionEvaluateInfoMissingField(t *testing.T) {
	a := checker.RedisAssertion{Command: "INFO", Section: "memory", Field: "inexistente", Op: "gt", Value: 0}

	got := a.Evaluate(infoMemory)

	if !strings.Contains(got, "INFO memory inexistente") || !strings.Contains(got, "não existe") {
		t.Errorf("Evaluate = %q, want a cause saying the field does not exist", got)
	}
}
