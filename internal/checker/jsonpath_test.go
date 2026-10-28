package checker

import (
	"reflect"
	"strings"
	"testing"
)

// O subconjunto é pequeno de propósito: só navegação até um valor. Cada
// forma aceita aqui é uma promessa de compatibilidade, e filtro ou
// wildcard devolveriam vários valores — o que obrigaria a decidir se
// "eq" vale para todos ou para algum, uma decisão que ninguém pediu.
func TestParseJSONPath(t *testing.T) {
	tests := []struct {
		path string
		want []pathSegment
	}{
		{"$", nil},
		{"$.db", []pathSegment{key("db")}},
		{"$.a.b", []pathSegment{key("a"), key("b")}},
		{"$.a[0].b", []pathSegment{key("a"), index(0), key("b")}},
		{"$[12]", []pathSegment{index(12)}},
		{"$.a[0][1]", []pathSegment{key("a"), index(0), index(1)}},
		{`$["chave com espaço"]`, []pathSegment{key("chave com espaço")}},
		{`$["a.b"].c`, []pathSegment{key("a.b"), key("c")}},
		{`$["aspas \" e \\ barra"]`, []pathSegment{key(`aspas " e \ barra`)}},
		{`$[""]`, []pathSegment{key("")}},
		{"$.queue_lag", []pathSegment{key("queue_lag")}},
		{"$.x-request-id", []pathSegment{key("x-request-id")}},
		{"$.ação", []pathSegment{key("ação")}},
		{"$.123", []pathSegment{key("123")}},
		{`$["😀"]`, []pathSegment{key("😀")}},
		{`$["٣"]`, []pathSegment{key("٣")}},
		// Barra escapada seguida de "ud800" é texto, não surrogate.
		{`$["\\ud800"]`, []pathSegment{key(`\ud800`)}},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := parseJSONPath(tt.path)
			if err != nil {
				t.Fatalf("parseJSONPath(%q) returned unexpected error: %v", tt.path, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseJSONPath(%q) = %#v, want %#v", tt.path, got, tt.want)
			}
		})
	}
}

// Recusar no cadastro é a única chance: um caminho que o parser "entende
// do jeito dele" aponta para outro campo e a asserção passa a vigiar a
// coisa errada sem que ninguém perceba.
func TestParseJSONPathRejectsMalformed(t *testing.T) {
	for _, path := range []string{
		"",
		"db",
		".db",
		"$.",
		"$..db",
		"$.a.",
		"$db",
		"$.a b",
		"$[",
		"$[]",
		"$[-1]",
		"$[01]",
		"$[1",
		"$[a]",
		"$[*]",
		"$.*",
		"$[?(@.x)]",
		"$[0:2]",
		`$["sem fim]`,
		`$["a"`,
		`$['simples']`,
		`$["a" ]`,
		"$[99999999999999999999]",
		"$ .a",
		// encoding/json troca um surrogate solto por U+FFFD em silêncio:
		// o caminho cadastrado apontaria para uma chave diferente da
		// escrita, e a forma canônica mostraria um caractere que ninguém
		// digitou.
		`$["\ud800"]`,
		`$["\udc00"]`,
		`$["a\ud800b"]`,
		`$["\ud800A"]`,
		// Depois do ponto só dígitos ASCII. unicode.IsDigit aceita ٣ e ３,
		// que se parecem com 3 num aviso e apontam para outra chave; quem
		// precisa deles escreve entre aspas.
		"$.a٣",
		"$.３",
	} {
		t.Run(path, func(t *testing.T) {
			if got, err := parseJSONPath(path); err == nil {
				t.Errorf("parseJSONPath(%q) = %#v, want an error", path, got)
			}
		})
	}
}

// O parser recebe texto livre do cadastro. Ele nunca pode entrar em
// pânico (o ValidateConfig derrubaria a requisição inteira), e todo
// caminho aceito precisa ter uma forma canônica que volte ao mesmo
// caminho — senão a mensagem de falha mostraria um caminho que, colado de
// volta no cadastro, aponta para outro lugar.
func FuzzParseJSONPath(f *testing.F) {
	for _, seed := range []string{
		"$", "$.a", "$.a.b", "$.a[0].b", `$["chave com espaço"]`, `$["a\"b"]`,
		"$[0][1]", "$.ação", "$[", `$["`, "$..a", "$[-1]", "$[*]", `$["ç"]`,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		segs, err := parseJSONPath(path)
		if err != nil {
			return
		}
		canonical := formatJSONPath(segs)
		again, err := parseJSONPath(canonical)
		if err != nil {
			t.Fatalf("canonical form %q of %q does not parse: %v", canonical, path, err)
		}
		if !reflect.DeepEqual(segs, again) {
			t.Fatalf("round trip of %q changed it: %#v -> %q -> %#v", path, segs, canonical, again)
		}
	})
}

// Caminho muito longo não pode virar alocação descontrolada no cadastro.
func TestParseJSONPathRejectsExcessiveLength(t *testing.T) {
	path := "$" + strings.Repeat(".a", maxJSONPathLen)

	if _, err := parseJSONPath(path); err == nil {
		t.Error("parseJSONPath of an oversized path returned nil error, want an error")
	}
}

func key(k string) pathSegment { return pathSegment{key: k, isKey: true} }
func index(i int) pathSegment  { return pathSegment{index: i} }
