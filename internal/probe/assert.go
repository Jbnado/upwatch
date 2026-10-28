package probe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Jbnado/upwatch/internal/checker"
)

// operatorNames traduz o símbolo da linha no operador da configuração.
// Os nomes da API também valem, para quem copia de um JSON existente.
var operatorNames = map[string]string{
	"==": "eq", "=": "eq", "!=": "ne", "<": "lt", "<=": "lte", ">": "gt", ">=": "gte",
	"eq": "eq", "ne": "ne", "lt": "lt", "lte": "lte", "gt": "gt", "gte": "gte",
}

// operatorSymbols é o caminho de volta, usado ao mostrar a asserção.
var operatorSymbols = map[string]string{
	"eq": "==", "ne": "!=", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
}

// ParseRedisAssertion lê uma asserção escrita numa linha, como
// "SCARD rq:workers:default >= 1".
//
// A forma é COMANDO [chave] [campo] OPERADOR VALOR, e quantos argumentos
// cabem entre o comando e o operador depende do comando: nenhum em
// DBSIZE, chave e campo em HGET, seção e campo em INFO, só a chave nos
// demais. O resultado passa pela validação do próprio checker, para a
// sonda não ser um segundo caminho até um comando fora da lista fechada.
func ParseRedisAssertion(line string) (checker.RedisAssertion, error) {
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return checker.RedisAssertion{}, fmt.Errorf(
			"asserção %q incompleta; use COMANDO [chave] OPERADOR VALOR, como SCARD rq:workers:default >= 1", line)
	}

	op, ok := operatorNames[strings.ToLower(parts[len(parts)-2])]
	if !ok {
		return checker.RedisAssertion{}, fmt.Errorf(
			"operador %q desconhecido; use ==, !=, <, <=, > ou >=", parts[len(parts)-2])
	}
	value, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return checker.RedisAssertion{}, fmt.Errorf("valor %q não é número", parts[len(parts)-1])
	}

	a := checker.RedisAssertion{Command: strings.ToUpper(parts[0]), Op: op, Value: value}
	args := parts[1 : len(parts)-2]
	switch {
	case a.Command == "DBSIZE" && len(args) == 0:
	case a.Command == "INFO" && len(args) == 2:
		a.Section, a.Field = args[0], args[1]
	case a.Command == "HGET" && len(args) == 2:
		a.Key, a.Field = args[0], args[1]
	case a.Command != "DBSIZE" && a.Command != "INFO" && a.Command != "HGET" && len(args) == 1:
		a.Key = args[0]
	default:
		return checker.RedisAssertion{}, fmt.Errorf(
			"%s com %d argumento(s) antes do operador; confira a forma do comando", a.Command, len(args))
	}

	if err := a.Validate(); err != nil {
		return checker.RedisAssertion{}, err
	}
	return a, nil
}

// FormatRedisAssertion escreve a asserção na mesma forma que
// ParseRedisAssertion lê.
func FormatRedisAssertion(a checker.RedisAssertion) string {
	parts := []string{strings.ToUpper(a.Command)}
	switch strings.ToUpper(a.Command) {
	case "DBSIZE":
	case "INFO":
		parts = append(parts, a.Section, a.Field)
	case "HGET":
		parts = append(parts, a.Key, a.Field)
	default:
		parts = append(parts, a.Key)
	}
	symbol := operatorSymbols[a.Op]
	if symbol == "" {
		symbol = a.Op
	}
	parts = append(parts, symbol, strconv.FormatFloat(a.Value, 'f', -1, 64))
	return strings.Join(parts, " ")
}
