package checker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSONAssertion é uma condição sobre um valor do corpo JSON da resposta.
//
// Value fica cru para preservar o número exatamente como foi escrito: um
// identificador acima de 2^53 não sobrevive a float64.
type JSONAssertion struct {
	Path  string          `json:"path"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Operadores aceitos em JSONAssertion.Op.
const (
	opEq       = "eq"
	opNe       = "ne"
	opLt       = "lt"
	opLte      = "lte"
	opGt       = "gt"
	opGte      = "gte"
	opContains = "contains"
	opExists   = "exists"
	opAbsent   = "absent"
)

// opSymbols é também a lista de operadores válidos; o símbolo é o que a
// causa mostra, porque "esperado < 100" se lê mais rápido que "esperado
// lt 100".
var opSymbols = map[string]string{
	opEq: "=", opNe: "≠", opLt: "<", opLte: "≤", opGt: ">", opGte: "≥",
	opContains: "contém", opExists: "existe", opAbsent: "ausente",
}

// maxShownValue é quanto do valor esperado entra na causa. A causa vai
// para chat e webhook de terceiros; um "contains" com um parágrafo
// inteiro não pode despejá-lo lá.
const maxShownValue = 80

// maxEchoedExpected é até onde um texto esperado é curto o bastante para
// a causa de eq e ne dizer só "diferente do esperado". Acima disso a
// causa mostra o tamanho do encontrado, que ajuda a ver o que mudou.
const maxEchoedExpected = 40

// compiledAssertion é a asserção já interpretada, para o corpo de cada
// verificação não repetir o trabalho do cadastro.
type compiledAssertion struct {
	JSONAssertion
	segs     []pathSegment
	expected any
}

// compileAssertion valida e interpreta a asserção. Os erros dizem o que
// está errado sem citar a posição na lista; quem chama acrescenta.
func compileAssertion(a JSONAssertion) (compiledAssertion, error) {
	segs, err := parseJSONPath(a.Path)
	if err != nil {
		return compiledAssertion{}, fmt.Errorf("caminho %q inválido: %v", a.Path, err)
	}
	if _, ok := opSymbols[a.Op]; !ok {
		return compiledAssertion{}, fmt.Errorf(
			"operador %q desconhecido; use eq, ne, lt, lte, gt, gte, contains, exists ou absent", a.Op)
	}

	c := compiledAssertion{JSONAssertion: a, segs: segs}
	hasValue := len(a.Value) > 0

	if a.Op == opExists || a.Op == opAbsent {
		if hasValue {
			return c, fmt.Errorf(`%q não leva valor; para exigir que o caminho não exista use "absent", `+
				`e para exigir o valor null use "eq" com null`, a.Op)
		}
		return c, nil
	}
	if !hasValue {
		return c, fmt.Errorf("%q precisa de um valor", a.Op)
	}
	if c.expected, err = decodeJSON(a.Value); err != nil {
		return c, fmt.Errorf("valor inválido: %v", err)
	}
	if n, ok := firstNumberWhere(c.expected, outOfComparableRange); ok {
		return c, fmt.Errorf("número %s fora da faixa comparável: até %d caracteres e expoente entre -%d e %d",
			show(n), maxNumberLen, maxNumberExp, maxNumberExp)
	}
	if n, ok := firstNumberWhere(c.expected, changesInFloat64); ok {
		return c, fmt.Errorf("número %s não cabe exato em ponto flutuante de 64 bits, que é como a interface o "+
			"lê; ele voltaria alterado na próxima gravação pela tela. Use um valor que caiba, ou compare como texto", show(n))
	}
	if isOrdering(a.Op) {
		if _, ok := c.expected.(json.Number); !ok {
			return c, fmt.Errorf("%q compara só números, e o valor é %s", a.Op, typeName(c.expected))
		}
	}
	return c, nil
}

// compileAssertions valida a lista inteira, apontando a posição de quem
// falhar — numa lista de dez, "valor inválido" sozinho não ajuda.
func compileAssertions(as []JSONAssertion) ([]compiledAssertion, error) {
	out := make([]compiledAssertion, 0, len(as))
	for i, a := range as {
		c, err := compileAssertion(a)
		if err != nil {
			return nil, fmt.Errorf("checker: json_assertions[%d] (asserção %d, %s): %w", i, i+1, a.Path, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// evaluateJSONAssertions devolve a causa da primeira asserção que falhar,
// ou string vazia se todas passam. truncated diz se o corpo foi cortado
// no teto de leitura.
func evaluateJSONAssertions(as []compiledAssertion, body []byte, truncated bool) string {
	doc, err := decodeJSON(body)
	if err != nil {
		if truncated {
			return fmt.Sprintf("corpo maior que o limite de leitura (%d bytes); o JSON lido está incompleto", MaxBodyBytes)
		}
		return fmt.Sprintf("corpo não é JSON válido: %v", err)
	}

	for _, a := range as {
		if msg := a.evaluate(doc); msg != "" {
			return msg
		}
	}
	return ""
}

// evaluate devolve a causa da falha, ou string vazia se a asserção vale.
//
// Caminho ausente é diferente de qualquer valor, null incluído: por isso
// "ne" passa e "eq null" reprova quando o caminho não existe. Só absent
// exige a ausência.
func (a compiledAssertion) evaluate(doc any) string {
	found, ok := lookupJSONPath(doc, a.segs)
	if !ok {
		switch a.Op {
		case opAbsent, opNe:
			return ""
		case opExists:
			return fmt.Sprintf("%s não existe", a.Path)
		}
		return fmt.Sprintf("%s não existe, %s", a.Path, a.expectation())
	}

	if a.Op != opExists && a.Op != opAbsent {
		if _, ok := firstNumberWhere(found, outOfComparableRange); ok {
			return fmt.Sprintf("%s: número fora da faixa comparável (mais de %d caracteres ou expoente além de ±%d)",
				a.Path, maxNumberLen, maxNumberExp)
		}
	}

	switch a.Op {
	case opExists:
		return ""
	case opAbsent:
		return fmt.Sprintf("%s existe, esperado ausente", a.Path)
	case opEq:
		if typeName(found) != typeName(a.expected) {
			return fmt.Sprintf("%s = %s, %s (%s): tipos diferentes",
				a.Path, describeTyped(found), a.expectation(), typeName(a.expected))
		}
		if jsonEqual(found, a.expected) {
			return ""
		}
		if a.echoesExpected(found) {
			return fmt.Sprintf("%s diferente do esperado, %s", a.Path, a.expectation())
		}
	case opNe:
		if !jsonEqual(found, a.expected) {
			return ""
		}
		if a.echoesExpected(found) {
			return fmt.Sprintf("%s igual ao valor recusado, %s", a.Path, a.expectation())
		}
	case opLt, opLte, opGt, opGte:
		n, isNum := found.(json.Number)
		if !isNum {
			return fmt.Sprintf("%s = %s, %s: comparação só entre números",
				a.Path, describeTyped(found), a.expectation())
		}
		if holdsOrdering(a.Op, compareNumbers(n, a.expected.(json.Number))) {
			return ""
		}
	case opContains:
		holds, applicable := contains(found, a.expected)
		if !applicable {
			return fmt.Sprintf("%s = %s, %s: contains vale para texto ou lista",
				a.Path, describeTyped(found), a.expectation())
		}
		if holds {
			return ""
		}
	}
	return fmt.Sprintf("%s = %s, %s", a.Path, describe(found), a.expectation())
}

// echoesExpected diz se a causa de eq ou ne sobre um texto pode se
// resumir a "diferente do esperado": o esperado curto já diz tudo, e o
// encontrado — talvez um token — fica de fora.
func (a compiledAssertion) echoesExpected(found any) bool {
	_, foundText := found.(string)
	want, wantText := a.expected.(string)
	return foundText && wantText && utf8.RuneCountInString(want) <= maxEchoedExpected
}

// describe escreve o valor encontrado para a causa. A causa vai para chat
// e webhook de terceiros, e o campo vigiado pode ser um token ou um
// e-mail: texto aparece só como tamanho, e lista e objeto, que podem
// conter texto, só como contagem. Número, booleano e null não carregam
// segredo e são mostrados, porque "lag = 340" poupa abrir o endpoint.
func describe(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("texto de %d caracteres", utf8.RuneCountInString(x))
	case []any:
		return fmt.Sprintf("lista de %d itens", len(x))
	case map[string]any:
		return fmt.Sprintf("objeto com %d chaves", len(x))
	}
	return show(v)
}

// describeTyped é describe com o tipo explícito, para causas em que o
// tipo é o problema; texto, lista e objeto já o dizem na descrição.
func describeTyped(v any) string {
	switch v.(type) {
	case string, []any, map[string]any:
		return describe(v)
	}
	return fmt.Sprintf("%s (%s)", show(v), typeName(v))
}

// expectation escreve o lado esperado da causa: "esperado < 100".
func (a compiledAssertion) expectation() string {
	return fmt.Sprintf("esperado %s %s", opSymbols[a.Op], show(a.expected))
}

func isOrdering(op string) bool {
	return op == opLt || op == opLte || op == opGt || op == opGte
}

func holdsOrdering(op string, cmp int) bool {
	switch op {
	case opLt:
		return cmp < 0
	case opLte:
		return cmp <= 0
	case opGt:
		return cmp > 0
	default:
		return cmp >= 0
	}
}

// contains diz se found contém want, e se a pergunta se aplica ao tipo
// de found.
func contains(found, want any) (holds, applicable bool) {
	switch f := found.(type) {
	case string:
		w, ok := want.(string)
		return ok && strings.Contains(f, w), true
	case []any:
		for _, item := range f {
			if jsonEqual(item, want) {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

// decodeJSON decodifica um único documento JSON preservando números como
// json.Number. Texto depois do documento é recusado: dois objetos colados
// não são um corpo JSON, e avaliar só o primeiro esconderia o segundo.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("vazio")
		}
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("há conteúdo depois do documento")
	}
	return v, nil
}

// jsonEqual compara dois valores decodificados por decodeJSON. Números
// são comparados pelo valor, então 340 e 340.0 são iguais.
func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		return ok && compareNumbers(x, y) == 0
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !jsonEqual(xv, yv) {
				return false
			}
		}
		return true
	default:
		// string, bool e nil comparam direto.
		return a == b
	}
}

// numberPrecision é a mantissa usada na comparação. Dois decimais
// distintos de até maxNumberLen dígitos diferem, em termos relativos, em
// pelo menos 10^-100 (uns 2^-333); 512 bits ficam bem abaixo disso, então
// o arredondamento nunca junta dois valores diferentes. float64, com 53,
// já erra ids acima de 2^53.
const numberPrecision = 512

// Faixa dos números que entram numa comparação. big.Float satura perto
// de 10^646456992 — 1e999999999 e 1e999999998 viram o mesmo infinito — e
// recusa expoente além de int64; e converter custa tempo quadrático no
// número de dígitos, um segundo para um milhão deles. Nenhum health real
// chega perto destes tetos, e dentro deles a comparação é exata e barata.
const (
	maxNumberLen = 100
	maxNumberExp = 10000
)

// outOfComparableRange diz se n está fora da faixa. Confere o texto
// antes de qualquer conversão, para o custo ficar proporcional ao tamanho
// do corpo.
func outOfComparableRange(n json.Number) bool {
	s := n.String()
	if len(s) > maxNumberLen {
		return true
	}
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return false
	}
	exp, err := strconv.Atoi(s[i+1:])
	return err != nil || exp < -maxNumberExp || exp > maxNumberExp
}

// changesInFloat64 diz se n muda ao passar por float64 e voltar pela
// menor grafia que o representa — exatamente o que JSON.parse e
// JSON.stringify fazem na interface. 0.1 sobrevive; 9007199254740993 e
// 1e999 não. Espera n já dentro da faixa comparável.
func changesInFloat64(n json.Number) bool {
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil || math.IsInf(f, 0) {
		return true
	}
	want, ok1 := new(big.Rat).SetString(n.String())
	got, ok2 := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return !ok1 || !ok2 || want.Cmp(got) != 0
}

// firstNumberWhere procura, em v e no que ele contém, um número para o
// qual pred vale.
func firstNumberWhere(v any, pred func(json.Number) bool) (json.Number, bool) {
	switch x := v.(type) {
	case json.Number:
		return x, pred(x)
	case []any:
		for _, item := range x {
			if n, ok := firstNumberWhere(item, pred); ok {
				return n, true
			}
		}
	case map[string]any:
		for _, item := range x {
			if n, ok := firstNumberWhere(item, pred); ok {
				return n, true
			}
		}
	}
	return "", false
}

// compareNumbers devolve -1, 0 ou 1. Os dois chegam validados pelo
// decodificador de JSON e conferidos por outOfComparableRange — do cadastro, o
// esperado; de evaluate, o encontrado —, então o parse não falha nem
// satura.
func compareNumbers(a, b json.Number) int {
	x, _, _ := big.ParseFloat(a.String(), 10, numberPrecision, big.ToNearestEven)
	y, _, _ := big.ParseFloat(b.String(), 10, numberPrecision, big.ToNearestEven)
	return x.Cmp(y)
}

// typeName nomeia o tipo JSON em português, como aparece na causa.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "nulo"
	case bool:
		return "booleano"
	case json.Number:
		return "número"
	case string:
		return "texto"
	case []any:
		return "lista"
	case map[string]any:
		return "objeto"
	}
	return fmt.Sprintf("%T", v)
}

// show escreve o valor como JSON compacto, cortado em maxShownValue
// caracteres — nunca no meio de um caractere, que deixaria a causa com
// UTF-8 inválido e faria alguns destinos recusarem o aviso inteiro.
func show(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	s := strings.TrimSuffix(buf.String(), "\n")

	if utf8.RuneCountInString(s) <= maxShownValue {
		return s
	}
	cut := 0
	for i := 0; i < maxShownValue; i++ {
		_, size := utf8.DecodeRuneInString(s[cut:])
		cut += size
	}
	return s[:cut] + "…"
}

// JSONAssertionOperators lista os operadores aceitos, para a documentação
// da API ser conferida contra a decisão do checker.
func JSONAssertionOperators() []string {
	ops := make([]string, 0, len(opSymbols))
	for op := range opSymbols {
		ops = append(ops, op)
	}
	return ops
}
