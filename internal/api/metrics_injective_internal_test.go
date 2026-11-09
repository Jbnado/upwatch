package api

import (
	"strings"
	"testing"
)

// desescapa imita o que o Prometheus faz com um valor de rótulo ao ler a
// exposição: ele conhece exatamente três escapes e deixa o resto como
// está (model/textparse, lvalReplacer).
var desescapa = strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n")

// Dois nomes diferentes não podem produzir o mesmo rótulo.
//
// É esta a asserção que importa, e não "nenhum byte de controle sobrou":
// duas amostras com rótulos iguais fazem o Prometheus recusar a raspagem
// inteira, que é o desfecho que o escape existe para evitar. Um escape que
// colapsa entradas distintas reintroduz o problema pela porta de trás.
func TestQuoteIsInjective(t *testing.T) {
	nomes := []string{
		"svc",
		"svc\x1b", // ESC
		`svc\x1b`, // o texto literal barra-x-1-b
		"svc\r",   // CR
		`svc\r`,   // o texto literal barra-r
		"svc\n",   // LF
		`svc\n`,   // o texto literal barra-n
		"svc\x0b", // VT
		`svc\`,    // barra solta
		`svc\\`,   // duas barras
		"svc\"",   // aspa
		"pré-produção",
	}

	vistos := map[string]string{}
	valores := map[string]string{}
	for _, nome := range nomes {
		saída := quote(nome)
		if outro, ok := vistos[saída]; ok {
			t.Errorf("colisão de bytes na exposição: %q e %q produzem %s", outro, nome, saída)
		}
		vistos[saída] = nome

		// E também depois de o coletor desfazer os escapes: o Prometheus
		// compara valores de rótulo, não os bytes da linha.
		// Tira exatamente uma aspa de cada ponta: Trim comeria também a aspa
		// escapada do fim, e a colisão apareceria onde não há.
		valor := desescapa.Replace(saída[1 : len(saída)-1])
		if outro, ok := valores[valor]; ok {
			t.Errorf("colisão de valor: %q e %q viram o mesmo rótulo %q", outro, nome, valor)
		}
		valores[valor] = nome
	}
}
