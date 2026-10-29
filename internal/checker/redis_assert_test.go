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

// A sonda lê a asserção de uma linha de texto e precisa recusar o
// comando antes de interpretar o resto, para a mensagem falar do comando
// proibido e não da contagem de argumentos.
func TestRedisReadCommandFollowsTheClosedList(t *testing.T) {
	for _, cmd := range []string{"SCARD", "scard", "INFO", "DBSIZE", "WORKERS"} {
		if !checker.RedisReadCommand(cmd) {
			t.Errorf("RedisReadCommand(%q) = false", cmd)
		}
	}
	for _, cmd := range []string{"FLUSHALL", "SET", "KEYS", "EVAL", ""} {
		if checker.RedisReadCommand(cmd) {
			t.Errorf("RedisReadCommand(%q) = true", cmd)
		}
	}
}

// O Bull e o BullMQ não registram worker em chave nenhuma; a única marca
// de um worker vivo é o nome que a conexão dele recebe com CLIENT SETNAME.
// WORKERS conta essas conexões, e prefixo e fila vão juntos na chave,
// como aparecem nas chaves da própria fila (bull:excel-render:wait).
func TestRedisAssertionWorkersValidation(t *testing.T) {
	for _, a := range []checker.RedisAssertion{
		{Command: "WORKERS", Key: "bull:excel-render", Op: "gte", Value: 1},
		{Command: "workers", Key: "bull:excel-render", Op: "gte", Value: 1},
		// Prefixo com dois-pontos ({app}:bull) é comum em cluster; a fila
		// é o que vem depois do último, como no BullMQ, que recusa
		// dois-pontos no nome da fila.
		{Command: "WORKERS", Key: "{app}:bull:emails", Op: "gte", Value: 2},
		{Command: "WORKERS", Key: "bull:minha fila", Op: "gte", Value: 1},
	} {
		if err := a.Validate(); err != nil {
			t.Errorf("Validate(%+v) returned unexpected error: %v", a, err)
		}
	}

	for name, a := range map[string]checker.RedisAssertion{
		"sem chave":     {Command: "WORKERS", Op: "gte", Value: 1},
		"sem prefixo":   {Command: "WORKERS", Key: "excel-render", Op: "gte", Value: 1},
		"prefixo vazio": {Command: "WORKERS", Key: ":excel-render", Op: "gte", Value: 1},
		"fila vazia":    {Command: "WORKERS", Key: "bull:", Op: "gte", Value: 1},
		"com campo":     {Command: "WORKERS", Key: "bull:q", Field: "f", Op: "gte", Value: 1},
		// O prefixo vai cru no nome da conexão, e CLIENT SETNAME recusa
		// espaço e caractere de controle: a asserção nunca casaria e
		// ficaria eternamente em zero. A fila vai em base64 e não tem
		// essa restrição.
		"prefixo com espaço": {Command: "WORKERS", Key: "meu app:fila", Op: "gte", Value: 1},
		"prefixo com acento": {Command: "WORKERS", Key: "filá:fila", Op: "gte", Value: 1},
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, a)
		}
	}
}

// A chave não vai ao Redis: o comando é sempre CLIENT LIST, sem argumento
// tirado do cadastro, e a contagem acontece do lado de cá.
func TestRedisAssertionWorkersArgs(t *testing.T) {
	a := checker.RedisAssertion{Command: "WORKERS", Key: "bull:excel-render"}

	if got, want := a.Args(), []any{"CLIENT", "LIST"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

// Saída de CLIENT LIST com o que um Redis de produção costuma ter: os
// workers da fila vigiada nas duas bibliotecas, mais conexões que não
// podem entrar na conta. "ZXhjZWwtcmVuZGVy" é base64("excel-render").
const clientListWithWorkers = "" +
	// Bull v3: o nome é exatamente <prefixo>:<base64 da fila>.
	"id=3 addr=10.0.0.5:51000 laddr=10.0.0.2:6379 fd=8 name=bull:ZXhjZWwtcmVuZGVy age=90 idle=0 flags=b db=0 cmd=brpoplpush\n" +
	// BullMQ sem nome de worker: igual ao Bull.
	"id=4 addr=10.0.0.6:51001 laddr=10.0.0.2:6379 fd=9 name=bull:ZXhjZWwtcmVuZGVy age=80 idle=0 flags=b db=0 cmd=bzpopmin\n" +
	// BullMQ com nome de worker: o sufixo :w:<nome>.
	"id=5 addr=10.0.0.7:51002 laddr=10.0.0.2:6379 fd=10 name=bull:ZXhjZWwtcmVuZGVy:w:render-1 age=70 idle=0 flags=b db=0 cmd=bzpopmin\n" +
	// QueueEvents do BullMQ escuta a fila, mas não processa nada.
	"id=6 addr=10.0.0.8:51003 laddr=10.0.0.2:6379 fd=11 name=bull:ZXhjZWwtcmVuZGVy:qe age=60 idle=0 flags=N db=0 cmd=xread\n" +
	// Outra fila cujo base64 começa igual: "excel-render-pdf". O Bull v3
	// compara por prefixo e contaria este; aqui não pode.
	"id=7 addr=10.0.0.9:51004 laddr=10.0.0.2:6379 fd=12 name=bull:ZXhjZWwtcmVuZGVyLXBkZg== age=50 idle=0 flags=b db=0 cmd=brpoplpush\n" +
	// Mesma fila com outro prefixo.
	"id=8 addr=10.0.0.10:51005 laddr=10.0.0.2:6379 fd=13 name=staging:ZXhjZWwtcmVuZGVy age=40 idle=0 flags=b db=0 cmd=bzpopmin\n" +
	// A própria conexão do monitor, sem nome.
	"id=9 addr=10.0.0.11:51006 laddr=10.0.0.2:6379 fd=14 name= age=0 idle=0 flags=N db=0 cmd=client|list\n"

func TestRedisAssertionWorkersCountsBullAndBullMQ(t *testing.T) {
	a := checker.RedisAssertion{Command: "WORKERS", Key: "bull:excel-render", Op: "eq", Value: 3}

	if got := a.Evaluate(clientListWithWorkers); got != "" {
		t.Errorf("Evaluate = %q, want the three workers counted", got)
	}
}

// A causa vai para o push da sonda e para o alerta. CLIENT LIST traz
// endereço e nome de toda conexão do Redis — de outros sistemas também —
// e nada disso pode sair dali; só a contagem.
func TestRedisAssertionWorkersCauseCarriesOnlyTheCount(t *testing.T) {
	a := checker.RedisAssertion{Command: "WORKERS", Key: "bull:excel-render", Op: "gte", Value: 5}

	got := a.Evaluate(clientListWithWorkers)

	if want := "WORKERS bull:excel-render = 3, esperado >= 5"; got != want {
		t.Errorf("Evaluate = %q, want %q", got, want)
	}
	for _, leak := range []string{"10.0.0", "ZXhjZWwtcmVuZGVy", "render-1", "staging"} {
		if strings.Contains(got, leak) {
			t.Errorf("Evaluate = %q leaks %q from CLIENT LIST", got, leak)
		}
	}
}

func TestRedisAssertionWorkersNoneAlive(t *testing.T) {
	a := checker.RedisAssertion{Command: "WORKERS", Key: "bull:fila-parada", Op: "gte", Value: 1}

	if want, got := "WORKERS bull:fila-parada = 0, esperado >= 1", a.Evaluate(clientListWithWorkers); got != want {
		t.Errorf("Evaluate = %q, want %q", got, want)
	}
}
