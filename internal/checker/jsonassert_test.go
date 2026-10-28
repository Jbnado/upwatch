package checker

import (
	"strings"
	"testing"
	"time"
)

// hugeNumberBody é um corpo dentro do teto de leitura com um único número
// de um milhão de dígitos. Interpretar isso como big.Float custa da ordem
// de um segundo — quadrático no número de dígitos —, por verificação e por
// comparação, e qualquer alvo pode mandar.
func hugeNumberBody() []byte {
	return []byte(`{"n":1` + strings.Repeat("3", 1_000_000) + `}`)
}

func compileOne(t testing.TB, a JSONAssertion) []compiledAssertion {
	t.Helper()
	c, err := compileAssertions([]JSONAssertion{a})
	if err != nil {
		t.Fatalf("compileAssertions: %v", err)
	}
	return c
}

// O limite de tamanho do número é o que mantém o custo linear no corpo:
// ele é conferido antes de qualquer conversão. O teto de tempo é folgado
// (decodificar 1 MiB leva milissegundos) e ainda assim bem abaixo do
// segundo que a conversão custava.
func TestEvaluateHugeNumberIsCheap(t *testing.T) {
	as := compileOne(t, JSONAssertion{Path: "$.n", Op: opEq, Value: []byte("5")})
	body := hugeNumberBody()

	start := time.Now()
	msg := evaluateJSONAssertions(as, body, false)
	elapsed := time.Since(start)

	if !strings.Contains(msg, "número fora da faixa comparável") {
		t.Errorf("evaluate = %q, want an out-of-range cause", msg)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("evaluating a 1M-digit number took %v, want it under 250ms", elapsed)
	}
}

func BenchmarkEvaluateHugeNumber(b *testing.B) {
	as := compileOne(b, JSONAssertion{Path: "$.n", Op: opEq, Value: []byte("5")})
	body := hugeNumberBody()

	b.SetBytes(int64(len(body)))
	for b.Loop() {
		evaluateJSONAssertions(as, body, false)
	}
}
