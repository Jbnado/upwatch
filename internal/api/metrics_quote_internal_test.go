package api

import (
	"strings"
	"testing"
)

// quote escapava aspas, barra invertida e \n, e o comentário dizia que
// cobria "aspas ou quebra de linha". \r é quebra de linha também, e ESC
// não é nenhuma das duas mas reprograma o terminal de quem lê a exposição
// crua — que é o caso comum, porque /metrics é aberto por padrão.
//
// Não é forja de série: a aspa é escapada, então não há como fechar o
// valor do rótulo e abrir outro. É falsificação do que o humano vê.
func TestQuoteNeutralizesEveryControlCharacter(t *testing.T) {
	casos := map[string]string{
		"retorno de carro":   "svc\rtudo-ok",
		"escape do terminal": "svc\x1b[2J\x1b[Htudo-ok",
		"nulo":               "svc\x00oculto",
		"tabulação vertical": "svc\x0boculto",
		"backspace":          "svc\boculto",
		"delete":             "svc\x7foculto",
		"quebra de linha":    "svc\ntudo-ok",
		"aspas":              `svc"tudo-ok`,
		"barra invertida":    `svc\tudo-ok`,
	}

	for nome, valor := range casos {
		saída := quote(valor)
		for _, r := range saída {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s: quote deixou passar o byte de controle %#U: %q", nome, r, saída)
			}
		}
		// E o valor continua fechado entre aspas, com o conteúdo legível.
		if !strings.HasPrefix(saída, `"`) || !strings.HasSuffix(saída, `"`) {
			t.Errorf("%s: quote não devolveu um valor entre aspas: %q", nome, saída)
		}
		if !strings.Contains(saída, "svc") {
			t.Errorf("%s: quote comeu o conteúdo: %q", nome, saída)
		}
	}
}

// Texto comum, inclusive acentuado, não é mexido: a exposição é UTF-8 e o
// nome de monitor em português tem de sair legível.
func TestQuoteLeavesOrdinaryTextAlone(t *testing.T) {
	if got, want := quote("pré-produção / bff"), `"pré-produção / bff"`; got != want {
		t.Errorf("quote = %q, want %q", got, want)
	}
}
