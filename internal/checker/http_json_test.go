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
		{"igualdade", assertion("$.db", "eq", "down"), `$.db = "up", esperado = "down"`},
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

// O valor encontrado vai para o aviso, que vai para chat e webhook de
// terceiros. Uma asserção sobre um campo que traz o documento inteiro não
// pode despejá-lo lá — nem estourar o limite de tamanho do destino.
func TestHTTPJSONAssertionTruncatesFoundValue(t *testing.T) {
	big := strings.Repeat("x", 10_000)
	url := serveJSON(t, fmt.Sprintf(`{"dump":%q}`, big))

	got := checkAssertions(t, url, assertion("$.dump", "eq", "curto"))

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
	url := serveJSON(t, fmt.Sprintf(`{"dump":%q}`, big))

	got := checkAssertions(t, url, assertion("$.dump", "eq", "curto"))

	if !utf8.ValidString(got.Message) {
		t.Errorf("Message is not valid UTF-8: %q", got.Message)
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
