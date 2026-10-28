package checker_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// healthBody é o health de API mais comum na prática: um objeto com o
// estado de cada dependência e um número que só importa acima de um teto.
const healthBody = `{"status":"ok","db":"up","queue_lag":340,"ready":true,` +
	`"version":null,"regions":["sa-east-1","us-east-1"],` +
	`"workers":[{"name":"fila","alive":3}],"chave com espaço":"sim",` +
	`"big":9007199254740993}`

func serveJSON(t *testing.T, body string) string {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}).URL
}

func assertion(path, op string, value ...any) map[string]any {
	a := map[string]any{"path": path, "op": op}
	if len(value) > 0 {
		a["value"] = value[0]
	}
	return a
}

func checkAssertions(t *testing.T, url string, as ...map[string]any) checker.Result {
	t.Helper()
	return check(t, url, map[string]any{"json_assertions": as})
}

// A tabela cobre cada operador nos dois sentidos. Um operador testado só
// no caso que passa continua verde se a comparação for trocada por
// "sempre verdadeiro" — e é exatamente esse defeito que deixaria um
// monitor saudável enquanto a dependência que ele vigia está fora.
func TestHTTPJSONAssertionOperators(t *testing.T) {
	url := serveJSON(t, healthBody)

	tests := []struct {
		name string
		a    map[string]any
		want domain.Status
	}{
		{"eq texto igual", assertion("$.db", "eq", "up"), domain.StatusUp},
		{"eq texto diferente", assertion("$.db", "eq", "down"), domain.StatusDown},
		{"eq número", assertion("$.queue_lag", "eq", 340), domain.StatusUp},
		{"eq número em outra grafia", assertion("$.queue_lag", "eq", 340.0), domain.StatusUp},
		{"eq booleano", assertion("$.ready", "eq", true), domain.StatusUp},
		{"eq nulo", assertion("$.version", "eq", nil), domain.StatusUp},
		{"eq lista", assertion("$.regions", "eq", []string{"sa-east-1", "us-east-1"}), domain.StatusUp},
		{"ne diferente", assertion("$.db", "ne", "down"), domain.StatusUp},
		{"ne igual", assertion("$.db", "ne", "up"), domain.StatusDown},
		// Valor de outro tipo é, por definição, diferente: "status ne
		// down" precisa passar quando o campo virou null.
		{"ne de outro tipo", assertion("$.version", "ne", "down"), domain.StatusUp},
		{"lt passa", assertion("$.queue_lag", "lt", 1000), domain.StatusUp},
		{"lt falha", assertion("$.queue_lag", "lt", 100), domain.StatusDown},
		{"lt no limite", assertion("$.queue_lag", "lt", 340), domain.StatusDown},
		{"lte no limite", assertion("$.queue_lag", "lte", 340), domain.StatusUp},
		{"lte falha", assertion("$.queue_lag", "lte", 339), domain.StatusDown},
		{"gt passa", assertion("$.queue_lag", "gt", 0), domain.StatusUp},
		{"gt no limite", assertion("$.queue_lag", "gt", 340), domain.StatusDown},
		{"gte no limite", assertion("$.queue_lag", "gte", 340), domain.StatusUp},
		{"gte falha", assertion("$.queue_lag", "gte", 341), domain.StatusDown},
		{"contains em texto", assertion("$.status", "contains", "o"), domain.StatusUp},
		{"contains em texto ausente", assertion("$.status", "contains", "erro"), domain.StatusDown},
		{"contains em lista", assertion("$.regions", "contains", "us-east-1"), domain.StatusUp},
		{"contains em lista ausente", assertion("$.regions", "contains", "eu-west-1"), domain.StatusDown},
		{"exists presente", assertion("$.db", "exists"), domain.StatusUp},
		// null é um valor presente: "o campo existe" e "o campo tem valor"
		// são perguntas diferentes, e a segunda é eq/ne null.
		{"exists com null", assertion("$.version", "exists"), domain.StatusUp},
		{"exists ausente", assertion("$.cache", "exists"), domain.StatusDown},
		// Ausente é diferente de qualquer valor: "db ne down" precisa
		// passar quando o campo some, senão a única forma de dizer "não
		// pode estar down" exigiria também que o campo exista.
		{"ne ausente", assertion("$.cache", "ne", "down"), domain.StatusUp},
		// "eq null" pergunta pelo valor: o campo precisa estar lá. Quem
		// quer exigir que ele não exista usa absent.
		{"eq null ausente", assertion("$.cache", "eq", nil), domain.StatusDown},
		{"absent ausente", assertion("$.cache", "absent"), domain.StatusUp},
		{"absent presente", assertion("$.db", "absent"), domain.StatusDown},
		// null é valor presente, então absent reprova — é o espelho de
		// "exists com null".
		{"absent com null", assertion("$.version", "absent"), domain.StatusDown},
		{"absent em índice fora da lista", assertion("$.workers[5]", "absent"), domain.StatusUp},
		{"índice e campo", assertion("$.workers[0].alive", "gte", 1), domain.StatusUp},
		{"índice fora da lista", assertion("$.workers[5].alive", "exists"), domain.StatusDown},
		{"chave entre aspas", assertion(`$["chave com espaço"]`, "eq", "sim"), domain.StatusUp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkAssertions(t, url, tt.a)
			if got.Status != tt.want {
				t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, tt.want)
			}
		})
	}
}

// Identificadores acima de 2^53 deixam de ser exatos em float64: 2^53+1 e
// 2^53 viram o mesmo número. Comparar por float64 aprovaria uma asserção
// de igualdade sobre um id que mudou.
func TestHTTPJSONAssertionComparesLargeIntegersExactly(t *testing.T) {
	url := serveJSON(t, healthBody)

	// json.Number evita que o próprio teste arredonde o valor esperado.
	got := checkAssertions(t, url, assertion("$.big", "eq", json.Number("9007199254740992")))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v (%s), want %v: 2^53 must not equal 2^53+1", got.Status, got.Message, domain.StatusDown)
	}
}

// A causa vai para o aviso. "asserção falhou" obriga quem está de plantão
// a abrir o endpoint para descobrir o quê; a issue pede o formato
// "$.queue_lag = 340, esperado < 100".
func TestHTTPJSONAssertionFailureNamesAssertionAndFoundValue(t *testing.T) {
	url := serveJSON(t, healthBody)

	tests := []struct {
		name string
		a    map[string]any
		want string
	}{
		{"numérica", assertion("$.queue_lag", "lt", 100), "$.queue_lag = 340, esperado < 100"},
		// Texto encontrado nunca é ecoado: ver o teste de vazamento abaixo.
		{"igualdade de texto", assertion("$.db", "eq", "down"), `$.db diferente do esperado, esperado = "down"`},
		{"diferença de texto", assertion("$.db", "ne", "up"), `$.db igual ao valor recusado, esperado ≠ "up"`},
		{"contains em texto", assertion("$.status", "contains", "erro"), `$.status = texto de 2 caracteres, esperado contém "erro"`},
		{"contains em lista", assertion("$.regions", "contains", "eu-west-1"), `$.regions = lista de 2 itens, esperado contém "eu-west-1"`},
		{"booleano", assertion("$.ready", "eq", false), "$.ready = true, esperado = false"},
		{"nulo", assertion("$.version", "ne", nil), "$.version = null, esperado ≠ null"},
		{"ausente", assertion("$.cache", "exists"), "$.cache não existe"},
		{"ausente com operador", assertion("$.cache", "eq", "up"), `$.cache não existe, esperado = "up"`},
		{"presente com absent", assertion("$.queue_lag", "absent"), "$.queue_lag existe, esperado ausente"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkAssertions(t, url, tt.a)
			if got.Status != domain.StatusDown {
				t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
			}
			if got.Message != tt.want {
				t.Errorf("Message = %q, want %q", got.Message, tt.want)
			}
		})
	}
}

// "eq" entre tipos diferentes é quase sempre erro de quem configurou —
// "100" contra 100 — e não queda do alvo. A mensagem precisa dizer isso,
// senão parece que o valor mudou.
func TestHTTPJSONAssertionTypeMismatchIsExplained(t *testing.T) {
	url := serveJSON(t, healthBody)

	got := checkAssertions(t, url, assertion("$.queue_lag", "eq", "340"))

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	for _, want := range []string{"número", "texto"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("Message = %q, want it to name the type %q", got.Message, want)
		}
	}
}

// Comparação de ordem só faz sentido entre números. Aceitar "10" < "9"
// por ordem de texto aprovaria em silêncio o contrário do pretendido.
func TestHTTPJSONAssertionOrderingOnNonNumberIsDown(t *testing.T) {
	url := serveJSON(t, healthBody)

	got := checkAssertions(t, url, assertion("$.db", "lt", 100))

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(got.Message, "número") {
		t.Errorf("Message = %q, want it to say a number was expected", got.Message)
	}
}

// Todas precisam passar; a causa nomeia a que quebrou, não a primeira da
// lista.
func TestHTTPJSONAssertionsAllMustHold(t *testing.T) {
	url := serveJSON(t, healthBody)

	got := checkAssertions(t, url,
		assertion("$.db", "eq", "up"),
		assertion("$.queue_lag", "lt", 100),
	)

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.HasPrefix(got.Message, "$.queue_lag") {
		t.Errorf("Message = %q, want it to name the failing assertion", got.Message)
	}
}

// O valor esperado também vai para o aviso, e ele vem do cadastro sem
// limite de tamanho. Um "contains" com um parágrafo inteiro não pode
// despejá-lo no canal nem estourar o limite de tamanho do destino.
func TestHTTPJSONAssertionTruncatesShownValue(t *testing.T) {
	url := serveJSON(t, `{"dump":"curto"}`)

	got := checkAssertions(t, url, assertion("$.dump", "contains", strings.Repeat("x", 10_000)))

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if len(got.Message) > 300 {
		t.Errorf("len(Message) = %d, want it truncated to a short cause", len(got.Message))
	}
	if !strings.Contains(got.Message, "…") {
		t.Errorf("Message = %q, want an ellipsis marking the truncation", got.Message)
	}
}

// Truncar por byte pode partir um caractere ao meio, e o aviso sairia com
// UTF-8 inválido — que alguns destinos recusam inteiro.
func TestHTTPJSONAssertionTruncationKeepsValidUTF8(t *testing.T) {
	// Um caractere de um byte na frente desalinha os de dois bytes, para
	// qualquer corte por byte cair no meio de um deles.
	big := "a" + strings.Repeat("ç", 4_000)
	url := serveJSON(t, `{"dump":"curto"}`)

	got := checkAssertions(t, url, assertion("$.dump", "contains", big))

	if !utf8.ValidString(got.Message) {
		t.Errorf("Message is not valid UTF-8: %q", got.Message)
	}
}

// A causa vai para chat e webhook de terceiros, e o campo vigiado pode
// ser um token ou um e-mail. Texto encontrado aparece só como tipo e
// tamanho, em qualquer operador e também dentro de listas e objetos;
// números, booleanos e null não carregam segredo e continuam visíveis,
// porque "lag = 340" é o que poupa abrir o endpoint de madrugada.
func TestHTTPJSONAssertionCauseDoesNotLeakFoundText(t *testing.T) {
	const secret = "s3cr3t-abc"
	url := serveJSON(t, `{"token":"`+secret+`","user":{"email":"`+secret+`@x.io"},"tags":["`+secret+`"]}`)

	for name, a := range map[string]map[string]any{
		"eq curto":           assertion("$.token", "eq", "outro"),
		"eq longo":           assertion("$.token", "eq", strings.Repeat("y", 200)),
		"eq de outro tipo":   assertion("$.token", "eq", 1),
		"contains em texto":  assertion("$.token", "contains", "zzz"),
		"lt em texto":        assertion("$.token", "lt", 1),
		"absent":             assertion("$.token", "absent"),
		"eq em objeto":       assertion("$.user", "eq", map[string]any{"email": "a"}),
		"contains em objeto": assertion("$.user", "contains", "a"),
		"contains em lista":  assertion("$.tags", "contains", "a"),
	} {
		t.Run(name, func(t *testing.T) {
			got := checkAssertions(t, url, a)

			if got.Status != domain.StatusDown {
				t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
			}
			if strings.Contains(got.Message, secret) {
				t.Errorf("Message = %q leaks the found text", got.Message)
			}
		})
	}
}

// Uma página de erro HTML do proxy na frente da API é a falha mais comum
// de um health JSON. Precisa virar causa legível, nunca pânico do worker.
func TestHTTPJSONAssertionOnNonJSONBodyIsDown(t *testing.T) {
	for name, body := range map[string]string{
		"html":            "<html><body>502 Bad Gateway</body></html>",
		"vazio":           "",
		"json quebrado":   `{"db":`,
		"dois documentos": `{"db":"up"} {"db":"down"}`,
	} {
		t.Run(name, func(t *testing.T) {
			url := serveJSON(t, body)

			got := checkAssertions(t, url, assertion("$.db", "eq", "up"))

			if got.Status != domain.StatusDown {
				t.Fatalf("Status = %v, want %v", got.Status, domain.StatusDown)
			}
			if !strings.Contains(got.Message, "JSON") {
				t.Errorf("Message = %q, want it to say the body is not JSON", got.Message)
			}
		})
	}
}

// O corpo é lido até MaxBodyBytes. Um JSON maior chega cortado e não
// decodifica; dizer só "JSON inválido" mandaria investigar o alvo, quando
// o motivo é o teto do próprio monitor.
func TestHTTPJSONAssertionOnBodyPastReadLimitSaysSo(t *testing.T) {
	url := serveJSON(t, fmt.Sprintf(`{"pad":%q,"db":"up"}`, strings.Repeat("a", checker.MaxBodyBytes)))

	got := checkAssertions(t, url, assertion("$.db", "eq", "up"))

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v, want %v: content past the read limit must not be considered", got.Status, domain.StatusDown)
	}
	if !strings.Contains(got.Message, "limite") {
		t.Errorf("Message = %q, want it to mention the read limit", got.Message)
	}
}

// Sem asserções, o corpo não é interpretado: um monitor que só confere
// status não pode cair porque o alvo responde texto.
func TestHTTPWithoutJSONAssertionsIgnoresNonJSONBody(t *testing.T) {
	url := serveJSON(t, "ok")

	if got := check(t, url, nil); got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// ---------- validação de configuração ----------

// Asserção inválida precisa reprovar no cadastro. Descoberta só na
// execução, ela derruba o monitor e acorda alguém por erro de digitação.
func TestHTTPValidateConfigRejectsInvalidJSONAssertion(t *testing.T) {
	tests := []struct {
		name string
		a    map[string]any
	}{
		{"caminho sem $", assertion("db", "eq", "up")},
		{"caminho vazio", assertion("", "eq", "up")},
		{"caminho mal formado", assertion("$.db[", "eq", "up")},
		{"índice negativo", assertion("$.a[-1]", "exists")},
		{"wildcard", assertion("$.a[*]", "exists")},
		{"filtro", assertion("$.a[?(@.x)]", "exists")},
		{"operador desconhecido", assertion("$.db", "matches", "up")},
		{"operador vazio", assertion("$.db", "", "up")},
		{"lt com texto", assertion("$.lag", "lt", "100")},
		{"gte com booleano", assertion("$.lag", "gte", true)},
		{"eq sem valor", assertion("$.db", "eq")},
		{"contains sem valor", assertion("$.db", "contains")},
		// exists com valor sugere que a pessoa esperava "exists false";
		// aceitar e ignorar faria a asserção dizer o contrário do escrito.
		{"exists com valor", assertion("$.db", "exists", false)},
		// Mesma razão: "absent true" ou "absent false" seria ambíguo.
		{"absent com valor", assertion("$.db", "absent", true)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{tt.a}})

			if err := checker.NewHTTP().ValidateConfig(cfg); err == nil {
				t.Fatalf("ValidateConfig(%s) returned nil error, want an error", cfg)
			}
		})
	}
}

// A mensagem de recusa precisa dizer qual asserção, numa lista de dez.
func TestHTTPValidateConfigNamesTheInvalidAssertion(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{
		assertion("$.db", "eq", "up"),
		assertion("$.lag", "lt", "cem"),
	}})

	err := checker.NewHTTP().ValidateConfig(cfg)

	if err == nil {
		t.Fatal("ValidateConfig returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "$.lag") {
		t.Errorf("error = %q, want it to point at assertion 2 ($.lag)", err)
	}
}

func TestHTTPValidateConfigAcceptsValidJSONAssertions(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{
		assertion("$.db", "eq", "up"),
		assertion("$.db", "eq", nil),
		assertion("$.queue_lag", "lt", 100),
		assertion("$.queue_lag", "gte", 0.5),
		assertion("$.workers[0].alive", "ne", 0),
		assertion(`$["chave com espaço"]`, "contains", "s"),
		assertion("$.regions", "contains", map[string]any{"id": 1}),
		assertion("$", "exists"),
		assertion("$.debug", "absent"),
	}})

	if err := checker.NewHTTP().ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig returned unexpected error: %v", err)
	}
}

// Quem escreve "exists" com valor quase sempre queria "não existe". A
// recusa precisa apontar o operador que faz isso — e não sugerir ne ou
// "eq null", que reprovam justamente quando o campo não está lá.
func TestHTTPValidateConfigExistsWithValuePointsToAbsent(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{
		assertion("$.debug", "exists", false),
	}})

	err := checker.NewHTTP().ValidateConfig(cfg)

	if err == nil {
		t.Fatal("ValidateConfig returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), `"absent"`) {
		t.Errorf("error = %q, want it to suggest \"absent\"", err)
	}
}

// ---------- números fora da faixa ----------

// big.Float satura acima de ~2^(2^31): 1e999999999 e 1e999999998 viram o
// mesmo infinito e comparam iguais; com expoente além de int64 o parse
// falha e a comparação caía em ordem de texto, onde "1e99…" < "2". Um
// valor esperado assim nunca seria comparado direito, então nem entra.
func TestHTTPValidateConfigRejectsNumbersOutOfComparableRange(t *testing.T) {
	tests := []struct {
		name string
		a    map[string]any
	}{
		{"expoente gigante", assertion("$.n", "eq", json.Number("1e999999998"))},
		{"expoente além de int64", assertion("$.n", "lt", json.Number("1e99999999999999999999"))},
		{"expoente acima de 10000", assertion("$.n", "gt", json.Number("1e10001"))},
		{"expoente abaixo de -10000", assertion("$.n", "gt", json.Number("1e-10001"))},
		{"mais de 100 caracteres", assertion("$.n", "eq", json.Number("1"+strings.Repeat("0", 100)))},
		{"dentro de uma lista", assertion("$.l", "eq", []any{json.Number("1e999999998")})},
		{"dentro de um objeto", assertion("$.l", "contains", map[string]any{"x": json.Number("1e999999998")})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{tt.a}})

			err := checker.NewHTTP().ValidateConfig(cfg)

			if err == nil {
				t.Fatalf("ValidateConfig(%s) returned nil error, want an error", cfg)
			}
			if !strings.Contains(err.Error(), "fora da faixa") {
				t.Errorf("error = %q, want it to say the number is out of range", err)
			}
		})
	}
}

// O corpo vem de fora e não passa por cadastro. Um número que não dá para
// comparar direito precisa virar causa legível; comparar mesmo assim
// deixava "1e99999999999999999999 < 2" passar por ordem de texto.
func TestHTTPJSONAssertionOnBodyNumberOutOfRangeIsDown(t *testing.T) {
	tests := []struct {
		name string
		body string
		a    map[string]any
	}{
		{"expoente além de int64", `{"n":1e99999999999999999999}`, assertion("$.n", "lt", 2)},
		{"expoente acima de 10000", `{"n":1e10001}`, assertion("$.n", "gt", 0)},
		{"mais de 100 caracteres", `{"n":1` + strings.Repeat("0", 100) + `}`, assertion("$.n", "gt", 0)},
		{"ne também", `{"n":1e99999999999999999999}`, assertion("$.n", "ne", 2)},
		{"dentro de lista", `{"l":[1e99999999999999999999]}`, assertion("$.l", "contains", 2)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkAssertions(t, serveJSON(t, tt.body), tt.a)

			if got.Status != domain.StatusDown {
				t.Fatalf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDown)
			}
			if !strings.Contains(got.Message, "número fora da faixa comparável") {
				t.Errorf("Message = %q, want it to say the number is out of the comparable range", got.Message)
			}
		})
	}
}

// Só os números que a asserção compara precisam caber na faixa: um campo
// vizinho com um valor exótico não pode derrubar um monitor que nem olha
// para ele.
func TestHTTPJSONAssertionIgnoresOutOfRangeNumbersItDoesNotCompare(t *testing.T) {
	url := serveJSON(t, `{"db":"up","n":1e99999999999999999999}`)

	got := checkAssertions(t, url, assertion("$.db", "eq", "up"), assertion("$.n", "exists"))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Dentro da faixa a comparação precisa ser exata. Com 256 bits de
// mantissa (uns 77 dígitos), 10^99+1 e 10^99 viravam o mesmo número, e um
// eq aprovava um valor que mudou no último dígito.
func TestHTTPJSONAssertionComparesAllDigitsInRange(t *testing.T) {
	url := serveJSON(t, `{"n":1`+strings.Repeat("0", 98)+`1}`)

	got := checkAssertions(t, url, assertion("$.n", "eq", json.Number("1e99")))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v (%s), want %v: 10^99+1 must not equal 10^99", got.Status, got.Message, domain.StatusDown)
	}
}

// A interface lê a configuração com JSON.parse, que guarda número em
// float64: 9007199254740993 chega como ...992 e 1e999 como Infinity, que
// volta como null. Aceitar esses valores pela API faria a primeira
// gravação pela tela trocar a asserção sem ninguém mexer nela. O valor
// esperado precisa sobreviver à ida e volta por float64 — o encontrado no
// corpo, que nunca passa pela tela, continua comparado com todos os
// dígitos.
func TestHTTPValidateConfigRejectsExpectedNumbersFloat64CannotHold(t *testing.T) {
	for _, n := range []string{"9007199254740993", "1e999", "-1e999", "0.1000000000000000000001", "1e-400"} {
		t.Run(n, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{
				assertion("$.n", "eq", json.Number(n)),
			}})

			err := checker.NewHTTP().ValidateConfig(cfg)

			if err == nil {
				t.Fatalf("ValidateConfig(%s) returned nil error, want an error", cfg)
			}
			if !strings.Contains(err.Error(), "64 bits") {
				t.Errorf("error = %q, want it to explain the 64-bit limit", err)
			}
		})
	}
}

func TestHTTPValidateConfigAcceptsExpectedNumbersFloat64Holds(t *testing.T) {
	for _, n := range []string{"0.1", "340.0", "9007199254740992", "9007199254740994", "1e308", "-0", "5e-324", "1.5E+3"} {
		t.Run(n, func(t *testing.T) {
			cfg, _ := json.Marshal(map[string]any{"json_assertions": []map[string]any{
				assertion("$.n", "eq", json.Number(n)),
			}})

			if err := checker.NewHTTP().ValidateConfig(cfg); err != nil {
				t.Errorf("ValidateConfig(%s) returned unexpected error: %v", cfg, err)
			}
		})
	}
}
