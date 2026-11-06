package api

import (
	"strings"
	"testing"
)

// quote faz os três escapes que o formato de exposição define, e só eles.
//
// A primeira versão desta mudança acrescentava \r e \xNN para os outros
// bytes de controle, e era pior: o coletor conhece apenas \\, \" e \n e
// devolve o resto intacto, então ESC e o texto literal "\x1b" viravam o
// mesmo rótulo. O byte de controle é barrado na entrada, em
// domain.Monitor.Validate e domain.Group.Validate.
func TestQuoteEscapesWhatTheFormatDefines(t *testing.T) {
	casos := map[string]string{
		`svc"tudo-ok`:  `"svc\"tudo-ok"`,
		`svc\tudo-ok`:  `"svc\\tudo-ok"`,
		"svc\ntudo-ok": `"svc\ntudo-ok"`,
		"svc":          `"svc"`,
	}

	for entrada, querido := range casos {
		if got := quote(entrada); got != querido {
			t.Errorf("quote(%q) = %s, want %s", entrada, got, querido)
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

// A aspa continua fechada: sem isto, um nome fecharia o valor do rótulo e
// abriria outro, e aí sim haveria forja de série.
func TestQuoteClosesTheLabelValue(t *testing.T) {
	saída := quote(`a" ,outro="b`)
	if strings.Count(saída, `"`)-strings.Count(saída, `\"`) != 2 {
		t.Errorf("o valor não ficou fechado por exatamente duas aspas: %s", saída)
	}
}
