package checker

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxRedisAssertions limita quantas asserções um monitor de Redis carrega.
// Cada uma é um comando a mais por verificação no servidor monitorado.
const MaxRedisAssertions = 10

// maxRedisKeyLength é o maior nome de chave aceito. O Redis aceita chaves
// enormes, mas nenhuma chave que valha vigiar tem esse tamanho, e o nome
// vai parar em toda mensagem de causa.
const maxRedisKeyLength = 512

// redisCommand descreve um comando de leitura permitido e o que ele exige.
type redisCommand struct {
	key     bool // precisa de chave
	field   bool // precisa de campo (HGET, INFO)
	section bool // precisa de seção (INFO)
}

// redisReadCommands é a lista fechada do que uma asserção pode executar.
//
// Fechada de propósito: o monitor guarda a credencial de um Redis de
// produção, e aceitar comando livre o transformaria num cliente genérico
// com escrita nas mãos de quem cadastra monitores. Todos aqui são de
// leitura e de custo constante ou baixo — KEYS e SMEMBERS ficam de fora
// porque varrem a base inteira a cada verificação. WORKERS não é comando
// do Redis: vira CLIENT LIST, que custa proporcional ao número de
// conexões, não ao de chaves.
var redisReadCommands = map[string]redisCommand{
	"SCARD":  {key: true},
	"LLEN":   {key: true},
	"GET":    {key: true},
	"EXISTS": {key: true},
	"HGET":   {key: true, field: true},
	"ZCARD":  {key: true},
	"DBSIZE": {},
	"INFO":   {section: true, field: true},
	// A chave é <prefixo>:<fila> da fila Bull/BullMQ (ver workerNames).
	"WORKERS": {key: true},
}

// RedisReadCommandList é a lista fechada por extenso, para as mensagens de
// erro do checker e da sonda não divergirem quando um comando entrar.
const RedisReadCommandList = "SCARD, LLEN, GET, EXISTS, HGET, ZCARD, DBSIZE, INFO ou WORKERS"

// redisOperators traduz o operador da configuração no símbolo usado na
// mensagem de causa.
var redisOperators = map[string]string{
	"eq": "==", "ne": "!=", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
}

// infoName restringe seção e campo de INFO a identificadores, que é o que
// o Redis usa; qualquer outra coisa é erro de digitação.
var infoName = regexp.MustCompile(`^[a-z0-9_]+$`)

// RedisAssertion é uma condição numérica sobre um comando de leitura.
//
// O caso que a motivou: workers do RQ se registram num conjunto, e "SCARD
// rq:workers:default >= 1" diz se há alguém consumindo a fila — coisa que
// um PING, verde com todos os workers mortos, não diz.
type RedisAssertion struct {
	Command string `json:"command"`
	Key     string `json:"key,omitempty"`
	// Field é o campo do hash em HGET, ou o campo do INFO.
	Field string `json:"field,omitempty"`
	// Section é a seção do INFO (memory, clients, replication...).
	Section string  `json:"section,omitempty"`
	Op      string  `json:"op"`
	Value   float64 `json:"value"`
}

func (a RedisAssertion) command() string { return strings.ToUpper(a.Command) }

// RedisReadCommand informa se o comando está na lista fechada de leitura.
// Serve a quem monta a asserção a partir de texto e quer recusar o
// comando antes de interpretar o resto da linha.
func RedisReadCommand(name string) bool {
	_, ok := redisReadCommands[strings.ToUpper(name)]
	return ok
}

// Validate confere a asserção no cadastro.
func (a RedisAssertion) Validate() error {
	spec, ok := redisReadCommands[a.command()]
	if !ok {
		return fmt.Errorf("checker: comando %q não é permitido; use um de %s", a.Command, RedisReadCommandList)
	}
	if _, ok := redisOperators[a.Op]; !ok {
		return fmt.Errorf("checker: operador %q desconhecido; use eq, ne, lt, lte, gt ou gte", a.Op)
	}

	switch {
	case hasWireBreak(a.Key) || hasWireBreak(a.Field) || hasWireBreak(a.Section):
		return fmt.Errorf("checker: chave, campo e seção de %s não podem ter quebra de linha nem NUL", a.command())
	case spec.key && a.Key == "":
		return fmt.Errorf("checker: %s precisa de uma chave", a.command())
	case !spec.key && a.Key != "":
		return fmt.Errorf("checker: %s não recebe chave", a.command())
	case len(a.Key) > maxRedisKeyLength:
		return fmt.Errorf("checker: a chave passa de %d caracteres", maxRedisKeyLength)
	case spec.field && a.Field == "":
		return fmt.Errorf("checker: %s precisa de um campo", a.command())
	case !spec.field && a.Field != "":
		return fmt.Errorf("checker: %s não recebe campo", a.command())
	case spec.section && !infoName.MatchString(a.Section):
		return fmt.Errorf("checker: INFO precisa de uma seção como memory ou clients, recebido %q", a.Section)
	case !spec.section && a.Section != "":
		return fmt.Errorf("checker: %s não recebe seção", a.command())
	case spec.section && !infoName.MatchString(a.Field):
		return fmt.Errorf("checker: campo de INFO inválido %q", a.Field)
	case a.command() == "WORKERS":
		return validateWorkersKey(a.Key)
	}
	return nil
}

// validateWorkersKey confere o <prefixo>:<fila> de WORKERS.
func validateWorkersKey(key string) error {
	i := strings.LastIndex(key, ":")
	if i <= 0 || i == len(key)-1 {
		return fmt.Errorf("checker: WORKERS precisa de prefixo e fila, como bull:minha-fila; recebido %q", key)
	}
	// O prefixo entra cru no nome da conexão, e o Redis só aceita ali
	// caracteres visíveis do ASCII; com outro, nenhum worker teria esse
	// nome e a contagem ficaria em zero para sempre.
	for _, r := range key[:i] {
		if r < '!' || r > '~' {
			return fmt.Errorf("checker: o prefixo de WORKERS não pode ter espaço nem acento; recebido %q", key[:i])
		}
	}
	return nil
}

// Args monta o comando enviado ao Redis.
//
// Os argumentos saem da estrutura, nunca de texto digitado: a chave vai
// como um argumento só, então não existe forma de emendar outro comando.
func (a RedisAssertion) Args() []any {
	cmd := a.command()
	switch cmd {
	case "DBSIZE":
		return []any{cmd}
	case "INFO":
		return []any{cmd, a.Section}
	case "HGET":
		return []any{cmd, a.Key, a.Field}
	case "WORKERS":
		// A chave fica do lado de cá: a contagem é feita sobre a lista.
		return []any{"CLIENT", "LIST"}
	default:
		return []any{cmd, a.Key}
	}
}

// label descreve o comando na mensagem de causa.
func (a RedisAssertion) label() string {
	switch a.command() {
	case "DBSIZE":
		return "DBSIZE"
	case "INFO":
		return "INFO " + a.Section + " " + a.Field
	case "HGET":
		return "HGET " + a.Key + " " + a.Field
	default:
		return a.command() + " " + a.Key
	}
}

// Evaluate compara a resposta com o esperado e devolve a causa da falha,
// ou vazio quando a condição vale.
//
// reply é a resposta crua: inteiro para os contadores, texto para GET,
// HGET e INFO, nil para chave inexistente.
func (a RedisAssertion) Evaluate(reply any) string {
	got, cause := a.measure(reply)
	if cause != "" {
		return cause
	}
	if compare(got, a.Op, a.Value) {
		return ""
	}
	return fmt.Sprintf("%s = %s, esperado %s %s",
		a.label(), formatNumber(got), redisOperators[a.Op], formatNumber(a.Value))
}

// measure extrai o número da resposta.
func (a RedisAssertion) measure(reply any) (float64, string) {
	if a.command() == "WORKERS" {
		list, ok := reply.(string)
		if !ok {
			return 0, fmt.Sprintf("%s: resposta inesperada do tipo %T", a.label(), reply)
		}
		// Só o número sai daqui. A lista traz endereço e nome de toda
		// conexão do servidor, inclusive de outros sistemas, e a causa vai
		// para o painel, o alerta e o push da sonda.
		return float64(countWorkers(list, a.Key)), ""
	}
	if a.command() == "INFO" {
		text, _ := reply.(string)
		raw, ok := infoField(text, a.Field)
		if !ok {
			return 0, fmt.Sprintf("%s: o campo não existe na resposta", a.label())
		}
		reply = raw
	}

	switch v := reply.(type) {
	case nil:
		// Não vira zero: "contador >= 0" passaria com a chave apagada,
		// escondendo justamente a falha que se queria pegar.
		return 0, fmt.Sprintf("%s: a chave não existe", a.label())
	case int64:
		return float64(v), ""
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			// O valor em si fica de fora: a causa é lida por quem não tem
			// acesso ao Redis, e repeti-la faria de uma asserção de GET um
			// leitor de qualquer chave. O tamanho basta para diagnosticar.
			return 0, fmt.Sprintf("%s: a resposta não é número (%d bytes)", a.label(), len(v))
		}
		return n, ""
	default:
		return 0, fmt.Sprintf("%s: resposta inesperada do tipo %T", a.label(), reply)
	}
}

// countWorkers conta, na saída de CLIENT LIST, as conexões de worker da
// fila <prefixo>:<fila>.
//
// O Bull e o BullMQ não registram worker em chave nenhuma; cada worker dá
// à sua conexão bloqueante o nome <prefixo>:<base64 da fila> com CLIENT
// SETNAME, e o BullMQ acrescenta :w:<nome> quando o worker tem nome. É o
// mesmo critério de Queue.getWorkers nas duas bibliotecas:
//
//   - Bull v3, lib/worker.js (clientName e parseClientList);
//   - BullMQ, src/classes/queue-getters.ts (getWorkers) e o nome montado
//     em src/classes/worker.ts.
//
// A comparação é exata, e não por prefixo como no Bull v3: lá a fila
// "excel-render" contaria os workers de "excel-render-pdf", cujo base64
// começa igual, e a fila de QueueEvents do BullMQ (sufixo :qe) também
// entraria, sem processar nada.
func countWorkers(list, key string) int {
	i := strings.LastIndex(key, ":")
	base := key[:i] + ":" + base64.StdEncoding.EncodeToString([]byte(key[i+1:]))
	named := base + ":w:"

	n := 0
	for _, line := range strings.Split(list, "\n") {
		for _, field := range strings.Fields(line) {
			name, ok := strings.CutPrefix(field, "name=")
			if !ok {
				continue
			}
			if name == base || strings.HasPrefix(name, named) {
				n++
			}
			break
		}
	}
	return n
}

// explain descreve a falha do comando na causa.
//
// CLIENT LIST costuma ficar fora do usuário de ACL feito para ler chaves,
// e o "NOPERM" cru no alerta parece queda de worker: o operador iria
// olhar o lugar errado. A causa diz o que dar ao usuário.
func (a RedisAssertion) explain(err error) string {
	if a.command() == "WORKERS" {
		msg := err.Error()
		switch {
		case strings.HasPrefix(msg, "NOPERM"):
			return fmt.Sprintf("%s: a ACL do Redis não deixa o usuário do monitor rodar CLIENT LIST; dê +client|list a ele", a.label())
		case strings.Contains(strings.ToLower(msg), "unknown command"):
			return fmt.Sprintf("%s: CLIENT LIST não está disponível neste Redis (comando desligado ou renomeado, comum em serviço gerenciado)", a.label())
		}
	}
	return fmt.Sprintf("%s: %v", a.label(), err)
}

// infoField procura um campo na resposta de INFO, casando o nome inteiro
// para used_memory não ser confundido com used_memory_human.
func infoField(info, field string) (string, bool) {
	for _, line := range strings.Split(info, "\n") {
		name, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok && name == field {
			return value, true
		}
	}
	return "", false
}

func compare(got float64, op string, want float64) bool {
	switch op {
	case "eq":
		return got == want
	case "ne":
		return got != want
	case "lt":
		return got < want
	case "lte":
		return got <= want
	case "gt":
		return got > want
	case "gte":
		return got >= want
	}
	return false
}

func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
