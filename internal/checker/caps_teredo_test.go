package checker_test

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/checker"
)

// embeddedIPv4Prefixes existe porque um endereço IPv6 que embute um IPv4
// leva para dentro, e errar a extração abriria a rede interna. A lista
// tinha NAT64, 6to4, IPv4-compatível e SIIT — e não tinha Teredo, que é o
// mecanismo de transição irmão do 6to4 e embute IPv4 do mesmo jeito.
//
// Não era buraco vivo: alcançar a rede interna por Teredo exige um túnel
// Teredo no host, e a imagem é Alpine sem isso. Mas o item da release
// dizia "faixas IPv6 de transição e reservadas contam como internas", e
// estava incompleto.
func TestTeredoCountsAsInternal(t *testing.T) {
	casos := map[string]bool{
		// Teredo: 2001::/32. O exemplo da RFC 4380.
		"2001:0:4136:e378:8000:63bf:3fff:fdd2": true,
		"2001::1":                              true,
		// 6to4, que já estava.
		"2002:a00:5::": true,
		// NAT64, que já estava.
		"64:ff9b::a00:5": true,
		// E o vizinho de prefixo que NÃO é Teredo continua externo:
		// 2001:4860:: é do Google, e 2001::/32 só cobre 2001:0:*.
		"2001:4860:4860::8888": false,
		"2600::1":              false,
	}

	for texto, querido := range casos {
		addr, err := netip.ParseAddr(texto)
		if err != nil {
			t.Fatalf("ParseAddr(%q) returned unexpected error: %v", texto, err)
		}
		if got := checker.InternalAddr(addr); got != querido {
			t.Errorf("InternalAddr(%s) = %v, want %v", texto, got, querido)
		}
	}
}

// Os outros campos interpretados do checker HTTP têm teto — asserções
// JSON, JSONPath, consulta SQL. O body_regex não tinha, então o padrão ia
// até o limite do corpo da requisição.
//
// Não é ReDoS clássico: o regexp do Go é RE2, linear e sem backtracking. O
// que sobra é O(m·n) na máquina NFA, com m preso só ao teto do corpo e n
// até MaxBodyBytes, reexecutado a cada intervalo dentro do pool que
// precisa verificar todo o resto.
func TestBodyRegexHasALengthCap(t *testing.T) {
	grande, err := json.Marshal(map[string]any{
		"body_regex": strings.Repeat("a", checker.MaxBodyRegexLength+1),
	})
	if err != nil {
		t.Fatalf("Marshal returned unexpected error: %v", err)
	}
	if err := checker.NewHTTP().ValidateConfig(grande); err == nil {
		t.Errorf("ValidateConfig aceitou um body_regex de %d caracteres",
			checker.MaxBodyRegexLength+1)
	}

	// No limite continua valendo: o teto não pode recusar uso legítimo.
	ok, err := json.Marshal(map[string]any{
		"body_regex": strings.Repeat("a", checker.MaxBodyRegexLength),
	})
	if err != nil {
		t.Fatalf("Marshal returned unexpected error: %v", err)
	}
	if err := checker.NewHTTP().ValidateConfig(ok); err != nil {
		t.Errorf("ValidateConfig recusou um body_regex no limite: %v", err)
	}
}
