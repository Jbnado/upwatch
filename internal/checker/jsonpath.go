package checker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// maxJSONPathLen limita o caminho de uma asserção. Nenhum documento de
// health precisa de mais, e o teto impede que o cadastro aceite um texto
// arbitrariamente grande para interpretar.
const maxJSONPathLen = 512

// maxJSONPathIndex limita o índice de lista: acima disso não é um health
// de verdade, e o teto mantém o número dentro de um int em qualquer
// plataforma.
const maxJSONPathIndex = 1 << 20

// pathSegment é um passo do caminho: uma chave de objeto ou um índice de
// lista.
type pathSegment struct {
	key   string
	index int
	isKey bool
}

// parseJSONPath interpreta o subconjunto de JSONPath aceito nas asserções:
//
//	$              o documento inteiro
//	$.chave        chave de objeto (letras, dígitos, _ e -)
//	$["chave"]     chave qualquer, entre aspas duplas, com escapes de JSON
//	$[0]           índice de lista, a partir de zero
//
// Sem filtros, wildcards, fatias nem descida recursiva: cada caminho aponta
// para no máximo um valor, então "eq" nunca precisa decidir entre "todos"
// e "algum".
func parseJSONPath(path string) ([]pathSegment, error) {
	if len(path) > maxJSONPathLen {
		return nil, fmt.Errorf("caminho com mais de %d caracteres", maxJSONPathLen)
	}
	if !utf8.ValidString(path) {
		return nil, errors.New("caminho com UTF-8 inválido")
	}
	if !strings.HasPrefix(path, "$") {
		return nil, errors.New(`o caminho precisa começar com "$"`)
	}

	var segs []pathSegment
	rest := path[1:]
	for rest != "" {
		var (
			seg pathSegment
			err error
		)
		switch rest[0] {
		case '.':
			seg, rest, err = parseDotKey(rest[1:])
		case '[':
			seg, rest, err = parseBracket(rest[1:])
		default:
			err = fmt.Errorf("caractere inesperado %q; use .chave ou [índice]", firstRune(rest))
		}
		if err != nil {
			return nil, err
		}
		segs = append(segs, seg)
	}
	return segs, nil
}

// parseDotKey lê a chave depois de um ponto, até o próximo . ou [.
func parseDotKey(s string) (pathSegment, string, error) {
	end := strings.IndexFunc(s, func(r rune) bool { return !isKeyRune(r) })
	if end < 0 {
		end = len(s)
	}
	if end == 0 {
		if s == "" {
			return pathSegment{}, "", errors.New(`chave vazia depois de "."`)
		}
		return pathSegment{}, "", fmt.Errorf(
			`caractere %q não é aceito depois de "."; use ["chave"] para chaves com símbolos ou espaço`,
			firstRune(s))
	}
	return pathSegment{key: s[:end], isKey: true}, s[end:], nil
}

// parseBracket lê o conteúdo de [ ... ], já sem o colchete de abertura.
func parseBracket(s string) (pathSegment, string, error) {
	if strings.HasPrefix(s, `"`) {
		return parseQuotedKey(s)
	}

	end := strings.IndexByte(s, ']')
	if end < 0 {
		return pathSegment{}, "", errors.New(`"[" sem "]" correspondente`)
	}
	digits := s[:end]
	if digits == "" {
		return pathSegment{}, "", errors.New(`"[]" vazio; informe um índice ou uma chave entre aspas`)
	}
	if strings.Trim(digits, "0123456789") != "" {
		return pathSegment{}, "", fmt.Errorf(
			"%q não é um índice; só números a partir de zero, sem filtros nem wildcards", digits)
	}
	if len(digits) > 1 && digits[0] == '0' {
		return pathSegment{}, "", fmt.Errorf("índice %q com zero à esquerda", digits)
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxJSONPathIndex {
		return pathSegment{}, "", fmt.Errorf("índice %s grande demais", digits)
	}
	return pathSegment{index: n}, s[end+1:], nil
}

// parseQuotedKey lê ["chave"], com os escapes de uma string JSON.
func parseQuotedKey(s string) (pathSegment, string, error) {
	// Procura a aspa de fechamento pulando as escapadas; a decodificação
	// em si fica com encoding/json, para os escapes serem exatamente os de
	// JSON e não uma variação própria.
	i := 1
	for i < len(s) && s[i] != '"' {
		if s[i] == '\\' {
			i++
		}
		i++
	}
	if i >= len(s) {
		return pathSegment{}, "", errors.New("chave entre aspas sem aspa de fechamento")
	}

	if hasLoneSurrogate(s[1:i]) {
		return pathSegment{}, "", errors.New(
			`chave entre aspas com surrogate \u solto; um caractere fora do plano básico usa o par inteiro`)
	}
	var k string
	if err := json.Unmarshal([]byte(s[:i+1]), &k); err != nil {
		return pathSegment{}, "", fmt.Errorf("chave entre aspas inválida: %v", err)
	}
	if !strings.HasPrefix(s[i+1:], "]") {
		return pathSegment{}, "", errors.New(`chave entre aspas precisa ser seguida de "]"`)
	}
	return pathSegment{key: k, isKey: true}, s[i+2:], nil
}

// hasLoneSurrogate diz se o texto entre aspas, ainda com os escapes, tem
// um \uD800–\uDFFF fora de um par alto+baixo. encoding/json troca um
// surrogate solto por U+FFFD sem reclamar, e o caminho passaria a apontar
// para uma chave diferente da escrita.
func hasLoneSurrogate(quoted string) bool {
	for i := 0; i < len(quoted); i++ {
		if quoted[i] != '\\' {
			continue
		}
		r, ok := unicodeEscape(quoted[i:])
		if !ok {
			i++ // pula o caractere escapado, que pode ser outra barra
			continue
		}
		i += 5
		if !utf16.IsSurrogate(r) {
			continue
		}
		if r >= 0xDC00 {
			return true
		}
		low, ok := unicodeEscape(quoted[i+1:])
		if !ok || low < 0xDC00 || low > 0xDFFF {
			return true
		}
		i += 6
	}
	return false
}

// unicodeEscape lê um \uXXXX no começo de s.
func unicodeEscape(s string) (rune, bool) {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return 0, false
	}
	n, err := strconv.ParseUint(s[2:6], 16, 16)
	return rune(n), err == nil
}

// formatJSONPath escreve o caminho na forma canônica: notação de ponto
// quando a chave permite, colchetes e aspas quando não.
func formatJSONPath(segs []pathSegment) string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range segs {
		switch {
		case !s.isKey:
			fmt.Fprintf(&b, "[%d]", s.index)
		case s.key != "" && strings.IndexFunc(s.key, func(r rune) bool { return !isKeyRune(r) }) < 0:
			b.WriteByte('.')
			b.WriteString(s.key)
		default:
			b.WriteByte('[')
			b.WriteString(quoteJSON(s.key))
			b.WriteByte(']')
		}
	}
	return b.String()
}

// quoteJSON escreve s como string JSON, sem trocar <, > e & por escapes —
// o resultado é lido por gente, não embutido em HTML.
func quoteJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // string nunca falha ao codificar
	return strings.TrimSuffix(buf.String(), "\n")
}

// isKeyRune diz o que pode vir depois do ponto. Dígito só ASCII:
// unicode.IsDigit aceita ٣ e ３, que num aviso se parecem com 3 e apontam
// para outra chave.
func isKeyRune(r rune) bool {
	return r == '_' || r == '-' || (r >= '0' && r <= '9') || unicode.IsLetter(r)
}

func firstRune(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	return string(r)
}

// lookupJSONPath percorre o documento decodificado. O segundo retorno é
// falso quando o caminho não existe — o que é diferente de existir com
// valor null.
func lookupJSONPath(doc any, segs []pathSegment) (any, bool) {
	cur := doc
	for _, s := range segs {
		if s.isKey {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = obj[s.key]; !ok {
				return nil, false
			}
			continue
		}
		list, ok := cur.([]any)
		if !ok || s.index >= len(list) {
			return nil, false
		}
		cur = list[s.index]
	}
	return cur, true
}
