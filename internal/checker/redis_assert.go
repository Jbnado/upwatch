package checker

import (
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
// porque varrem a base inteira a cada verificação.
var redisReadCommands = map[string]redisCommand{
	"SCARD":  {key: true},
	"LLEN":   {key: true},
	"GET":    {key: true},
	"EXISTS": {key: true},
	"HGET":   {key: true, field: true},
	"ZCARD":  {key: true},
	"DBSIZE": {},
	"INFO":   {section: true, field: true},
}

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

// Validate confere a asserção no cadastro.
func (a RedisAssertion) Validate() error {
	spec, ok := redisReadCommands[a.command()]
	if !ok {
		return fmt.Errorf("checker: comando %q não é permitido; use um de SCARD, LLEN, GET, EXISTS, HGET, ZCARD, DBSIZE ou INFO", a.Command)
	}
	if _, ok := redisOperators[a.Op]; !ok {
		return fmt.Errorf("checker: operador %q desconhecido; use eq, ne, lt, lte, gt ou gte", a.Op)
	}

	switch {
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
			return 0, fmt.Sprintf("%s = %q, não é número", a.label(), truncate(v, 64))
		}
		return n, ""
	default:
		return 0, fmt.Sprintf("%s: resposta inesperada do tipo %T", a.label(), reply)
	}
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
