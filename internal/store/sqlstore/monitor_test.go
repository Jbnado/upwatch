package sqlstore

import "testing"

// TestEscapeLike cobre o que a cláusula ESCAPE '\' exige do padrão.
//
// A barra invertida é o próprio caractere de escape, então ela precisa ser
// duplicada antes de tudo. Sem isso o padrão sai com escape a menos — um
// curinga volta a valer — ou com um escape pendurado no fim, que o
// PostgreSQL recusa com erro em vez de só trazer demais.
func TestEscapeLike(t *testing.T) {
	casos := map[string]string{
		"sem curinga": "sem curinga",
		"a_b":         `a\_b`,
		"100%":        `100\%`,
		`\`:           `\\`,
		`\%`:          `\\\%`,
		`\_`:          `\\\_`,
		`a\b_c`:       `a\\b\_c`,
	}
	for entrada, esperado := range casos {
		t.Run(entrada, func(t *testing.T) {
			if got := escapeLike(entrada); got != esperado {
				t.Fatalf("escapeLike(%q) = %q, esperado %q", entrada, got, esperado)
			}
		})
	}
}

// TestTagNeedle confere que o trecho procurado é a etiqueta como ela ficou
// guardada, e não como veio na requisição.
//
// As etiquetas moram num array JSON, onde a barra invertida aparece
// dobrada. Montar o padrão com a string crua faria o filtro procurar uma
// barra só e nunca achar a linha que ele mesmo gravou.
func TestTagNeedle(t *testing.T) {
	casos := map[string]string{
		"prod": `%"prod"%`,
		`\%`:   `%"\\\\\%"%`,
		`a"b`:  `%"a\\"b"%`,
	}
	for tag, esperado := range casos {
		t.Run(tag, func(t *testing.T) {
			got, err := tagNeedle(tag)
			if err != nil {
				t.Fatalf("tagNeedle(%q) devolveu erro: %v", tag, err)
			}
			if got != esperado {
				t.Fatalf("tagNeedle(%q) = %q, esperado %q", tag, got, esperado)
			}
		})
	}
}
