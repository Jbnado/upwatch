package notifier_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/notifier"
)

// Corpo mapeável: quem integra decide a forma do JSON.
//
// O envelope fixo serve para quem só quer receber o evento, mas raramente
// serve para um destino que já existe — um endpoint de automação costuma
// esperar os campos com os nomes dele, e adaptar o recebedor nem sempre é
// possível. O mapeamento resolve isso sem obrigar ninguém a manter um
// tradutor no meio do caminho.
//
// A substituição acontece sobre uma estrutura JSON já decodificada, e o
// resultado é serializado de volta — nunca por concatenação de texto. É o
// que garante que o corpo continue sendo JSON válido mesmo quando o nome
// do monitor tem aspas ou a causa tem quebra de linha.

// novoCanal monta um canal com o mapeamento de corpo dado.
//
// A configuração é montada como texto, e não por json.Marshal, para que um
// mapeamento malformado chegue inteiro ao cadastro — é justamente ali que
// ele precisa ser recusado.
func novoCanal(kind, url, mapeamento string) (notifier.Notifier, error) {
	cfg := `{"url":` + aspas(url)
	if mapeamento != "" {
		cfg += `,"body_template":` + mapeamento
	}
	cfg += `}`

	return notifier.Build(kind, json.RawMessage(cfg))
}

func novoWebhook(url, mapeamento string) (notifier.Notifier, error) {
	return novoCanal("webhook", url, mapeamento)
}

func novoWebhookComTexto(url, mapeamento, texto string) (notifier.Notifier, error) {
	cfg := `{"url":` + aspas(url) + `,"template":` + aspas(texto)
	if mapeamento != "" {
		cfg += `,"body_template":` + mapeamento
	}
	cfg += `}`

	return notifier.NewWebhook(json.RawMessage(cfg))
}

func aspas(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// entrega envia a notificação e devolve o corpo recebido, já decodificado.
func entrega(t *testing.T, cfg string, n notifier.Notification) map[string]any {
	t.Helper()

	cap, url := newCapture(t)
	w, err := novoWebhook(url, cfg)
	if err != nil {
		t.Fatalf("o cadastro recusou a configuração: %v", err)
	}
	if err := send(t, w, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	if err := json.Unmarshal([]byte(cap.body(0)), &corpo); err != nil {
		t.Fatalf("o corpo entregue não é JSON válido: %v\ncorpo: %s", err, cap.body(0))
	}
	return corpo
}

// Sem mapeamento, o corpo continua sendo o envelope de sempre.
//
// Quem já integrou contra a forma antiga não pode quebrar porque o
// recurso passou a existir.
func TestSemMapeamentoOCorpoNaoMuda(t *testing.T) {
	corpo := entrega(t, "", outage())

	for _, campo := range []string{
		"text", "monitor", "monitor_id", "target",
		"status", "previous_status", "message", "at", "duration_seconds",
	} {
		if _, ok := corpo[campo]; !ok {
			t.Errorf("o envelope padrão perdeu o campo %q", campo)
		}
	}
}

// O caso que motiva o recurso: os nomes de campo são de quem recebe.
func TestMapeamentoRenomeiaOsCampos(t *testing.T) {
	corpo := entrega(t, `{"event":"$status","service":"$monitor","detail":"$message"}`, outage())

	if corpo["event"] != "down" {
		t.Errorf("event = %v, want down", corpo["event"])
	}
	if corpo["service"] != "api de produção" {
		t.Errorf("service = %v, want o nome do monitor", corpo["service"])
	}
	if len(corpo) != 3 {
		t.Errorf("o corpo tem %d campos, want só os 3 declarados: %v", len(corpo), corpo)
	}
}

// Um marcador sozinho entrega o valor com o tipo dele.
//
// Se virasse texto, um recebedor que espera número receberia "1020" e
// falharia na validação de esquema — e o erro apareceria durante a queda,
// que é quando ninguém tem tempo de investigar.
func TestMarcadorSozinhoPreservaOTipo(t *testing.T) {
	corpo := entrega(t, `{"secs":"$duration_seconds","id":"$monitor_id"}`, recovery(17*time.Minute))

	secs, ok := corpo["secs"].(float64)
	if !ok {
		t.Fatalf("secs = %#v (%T), want número", corpo["secs"], corpo["secs"])
	}
	if secs != float64(17*60) {
		t.Errorf("secs = %v, want %v", secs, 17*60)
	}
	if _, ok := corpo["id"].(float64); !ok {
		t.Errorf("id = %#v (%T), want número", corpo["id"], corpo["id"])
	}
}

// Marcador no meio de um texto compõe uma frase, e o resultado é texto.
func TestMarcadorInterpoladoViraTexto(t *testing.T) {
	corpo := entrega(t, `{"resumo":"[$status] $monitor"}`, outage())

	if corpo["resumo"] != "[down] api de produção" {
		t.Errorf("resumo = %v, want a frase composta", corpo["resumo"])
	}
}

// A forma é livre: objetos aninhados e listas, porque destino nenhum tem
// obrigação de aceitar um objeto raso.
func TestFormaAninhadaEComListas(t *testing.T) {
	corpo := entrega(t, `{"alert":{"labels":{"sev":"$status"},"tags":["upwatch","$monitor"]}}`, outage())

	alert, ok := corpo["alert"].(map[string]any)
	if !ok {
		t.Fatalf("alert = %#v, want objeto", corpo["alert"])
	}
	labels, ok := alert["labels"].(map[string]any)
	if !ok || labels["sev"] != "down" {
		t.Errorf("alert.labels = %#v, want sev=down", alert["labels"])
	}

	tags, ok := alert["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "upwatch" || tags[1] != "api de produção" {
		t.Errorf("alert.tags = %#v, want [upwatch, nome do monitor]", alert["tags"])
	}
}

// Valores que não são texto passam intactos: eles são a parte fixa do
// contrato com o destino.
func TestLiteraisPassamIntactos(t *testing.T) {
	corpo := entrega(t, `{"versao":2,"critico":true,"extra":null,"status":"$status"}`, outage())

	if corpo["versao"] != float64(2) {
		t.Errorf("versao = %#v, want 2", corpo["versao"])
	}
	if corpo["critico"] != true {
		t.Errorf("critico = %#v, want true", corpo["critico"])
	}
	if v, ok := corpo["extra"]; !ok || v != nil {
		t.Errorf("extra = %#v (presente=%v), want null presente", v, ok)
	}
}

// $$ escapa o cifrão, senão não haveria como enviar um literal.
func TestCifraoDuploViraCifraoLiteral(t *testing.T) {
	corpo := entrega(t, `{"preco":"US$$ 10","estado":"$status"}`, outage())

	if corpo["preco"] != "US$ 10" {
		t.Errorf("preco = %v, want \"US$ 10\"", corpo["preco"])
	}
	if corpo["estado"] != "down" {
		t.Errorf("estado = %v, want down — o escape não pode desligar a substituição seguinte", corpo["estado"])
	}
}

// As chaves com delimitador resolvem a ambiguidade de onde o nome acaba.
func TestChavesDelimitamONomeDoMarcador(t *testing.T) {
	corpo := entrega(t, `{"a":"${status}X","b":"${monitor}"}`, outage())

	if corpo["a"] != "downX" {
		t.Errorf("a = %v, want downX", corpo["a"])
	}
	if corpo["b"] != "api de produção" {
		t.Errorf("b = %v, want o nome do monitor", corpo["b"])
	}
}

// A chave do objeto é do destino, não do evento: substituir ali produziria
// um esquema que muda de forma a cada notificação.
func TestChaveDeObjetoNaoSofreSubstituicao(t *testing.T) {
	corpo := entrega(t, `{"$status":"fixo"}`, outage())

	if _, ok := corpo["$status"]; !ok {
		t.Errorf("a chave foi substituída; o corpo veio %v", corpo)
	}
}

// O valor perigoso é o que vem do mundo: nome de monitor e causa de falha
// são texto livre. Se a substituição fosse por concatenação, aspas e
// quebras de linha produziriam um corpo que o destino rejeita — e o aviso
// da queda se perderia justamente por causa da queda.
func TestTextoHostilNaoQuebraOJSON(t *testing.T) {
	n := outage()
	n.Monitor.Name = `api "prod" \ zona`
	n.Message = "linha um\nlinha dois\ttab \"aspas\""

	corpo := entrega(t, `{"nome":"$monitor","causa":"$message"}`, n)

	if corpo["nome"] != n.Monitor.Name {
		t.Errorf("nome = %q, want %q", corpo["nome"], n.Monitor.Name)
	}
	if corpo["causa"] != n.Message {
		t.Errorf("causa = %q, want %q", corpo["causa"], n.Message)
	}
}

// Marcador desconhecido é erro de cadastro, não de entrega.
//
// Descobrir o erro de digitação durante o incidente é descobrir tarde: o
// aviso já não foi. Aqui ele aparece na hora de salvar o canal.
func TestMarcadorDesconhecidoERecusadoNoCadastro(t *testing.T) {
	_, url := newCapture(t)

	_, err := novoWebhook(url, `{"x":"$statsu"}`)
	if err == nil {
		t.Fatal("o cadastro aceitou um marcador que não existe")
	}
	if !strings.Contains(err.Error(), "statsu") {
		t.Errorf("a mensagem não diz qual marcador está errado: %v", err)
	}
}

// Mapeamento que não é JSON válido também morre no cadastro.
func TestMapeamentoInvalidoERecusadoNoCadastro(t *testing.T) {
	_, url := newCapture(t)

	if _, err := novoWebhook(url, `{"x":`); err == nil {
		t.Fatal("o cadastro aceitou um mapeamento que não é JSON")
	}
}

// Campo vazio continua presente: sumir com a chave muda a forma do corpo
// entre uma notificação e outra, e quem valida esquema quebra.
func TestCampoVazioContinuaPresente(t *testing.T) {
	corpo := entrega(t, `{"causa":"$message"}`, recovery(time.Minute))

	v, ok := corpo["causa"]
	if !ok {
		t.Fatalf("a chave sumiu quando a causa era vazia: %v", corpo)
	}
	if v != "" {
		t.Errorf("causa = %#v, want string vazia", v)
	}
}

// Os dois modelos convivem: o de texto compõe a frase, e $text a entrega
// dentro da forma escolhida.
func TestTextoModeladoChegaNoMarcadorText(t *testing.T) {
	cfg := `{"msg":"$text"}`
	cap, url := newCapture(t)

	w, err := novoWebhookComTexto(url, cfg, "{{.Monitor.Name}} caiu")
	if err != nil {
		t.Fatalf("o cadastro recusou: %v", err)
	}
	if err := send(t, w, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	var corpo map[string]any
	_ = json.Unmarshal([]byte(cap.body(0)), &corpo)

	if corpo["msg"] != "api de produção caiu" {
		t.Errorf("msg = %v, want a frase do modelo de texto", corpo["msg"])
	}
}

// Todo marcador anunciado precisa produzir alguma coisa.
//
// A lista exportada é o que a interface mostra a quem escreve o
// mapeamento. Anunciar um nome que não resolve seria pior que não
// anunciar: a pessoa escreveria o mapeamento contra ele e descobriria
// durante a queda.
func TestTodoMarcadorAnunciadoResolve(t *testing.T) {
	nomes := notifier.Marcadores()
	if len(nomes) == 0 {
		t.Fatal("nenhum marcador anunciado")
	}

	mapa := make(map[string]string, len(nomes))
	for _, nome := range nomes {
		mapa[nome] = "$" + nome
	}
	cfg, _ := json.Marshal(mapa)

	corpo := entrega(t, string(cfg), outage())

	for _, nome := range nomes {
		v, ok := corpo[nome]
		if !ok {
			t.Errorf("o marcador $%s é anunciado mas não chegou no corpo", nome)
			continue
		}
		if s, texto := v.(string); texto && strings.HasPrefix(s, "$") {
			t.Errorf("o marcador $%s não foi resolvido: veio %q", nome, s)
		}
	}
}

// Sem modelo de texto, $text entrega a frase padrão — a mesma que o canal
// enviaria sem mapeamento nenhum.
func TestTextPadraoChegaNoMarcador(t *testing.T) {
	corpo := entrega(t, `{"msg":"$text"}`, outage())

	msg, _ := corpo["msg"].(string)
	if !strings.Contains(msg, "está fora do ar") {
		t.Errorf("msg = %q, want a frase padrão da queda", msg)
	}
}

// Discord e Slack não aceitam mapeamento.
//
// O corpo deles é ditado pelo destino: um objeto com a forma trocada é
// recusado do outro lado, e o canal passaria a falhar sem que a interface
// tivesse avisado nada. Recusar no cadastro é mais honesto que aceitar uma
// configuração que não pode funcionar.
func TestCanaisDeFormatoFixoRecusamMapeamento(t *testing.T) {
	_, url := newCapture(t)

	for _, canal := range []string{"discord", "slack"} {
		t.Run(canal, func(t *testing.T) {
			if _, err := novoCanal(canal, url, `{"x":"$status"}`); err == nil {
				t.Errorf("%s aceitou um mapeamento de corpo", canal)
			}
		})
	}
}
