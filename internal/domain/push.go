package domain

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MaxPushMessageLength é o tamanho máximo, em caracteres, da causa
// informada por quem reporta via push.
//
// A mensagem vem de fora e acaba em aviso de chat e em e-mail. O limite
// cabe uma frase de diagnóstico e impede que um script despeje um log
// inteiro em cada notificação.
const MaxPushMessageLength = 200

// MaxPushLatencyMS é o teto da latência informada: um dia.
//
// Nenhuma ida e volta legítima passa disso, e o valor vira time.Duration
// nas contas de degradação — multiplicado por um milhão, um número
// absurdo estoura o int64 e volta negativo, e o monitor passaria a
// parecer rapidíssimo justamente quando a medição não faz sentido.
const MaxPushLatencyMS = int64(24 * 60 * 60 * 1000)

// PushSignal é o último sinal recebido de um monitor push.
//
// Carrega o que o próprio serviço sabe sobre si: quem reporta por push
// muitas vezes sabe exatamente o que deu errado, e reduzir o sinal a "estou
// vivo" o obrigaria a escolher entre mentir e calar.
type PushSignal struct {
	// ID identifica o sinal entre os do mesmo monitor. É o que permite
	// contá-lo uma vez só: dois sinais no mesmo milissegundo são dois.
	ID int64

	// Evaluated diz se alguma verificação já contou este sinal.
	Evaluated bool

	At        time.Time
	Status    Status
	Message   string
	LatencyMS int64
}

// ParsePushStatus interpreta o status informado no sinal.
//
// Vazio vale "up", para o sinal sem parâmetros manter o significado que
// sempre teve. "unknown" é recusado: é o que o UpWatch diz quando não sabe,
// e as contas de disponibilidade ignoram essas amostras.
func ParsePushStatus(raw string) (Status, error) {
	switch raw {
	case "":
		return StatusUp, nil
	case "up":
		return StatusUp, nil
	case "down":
		return StatusDown, nil
	case "degraded":
		return StatusDegraded, nil
	}
	return StatusUnknown, invalid("status", "status inválido; use up, down ou degraded")
}

// ParsePushLatency interpreta a duração medida pelo próprio serviço.
//
// Vazio é zero, que no resto do sistema já significa "não medido".
func ParsePushLatency(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms < 0 {
		return 0, invalid("latency_ms", "latência inválida; use milissegundos inteiros, sem sinal")
	}
	if ms > MaxPushLatencyMS {
		return 0, invalid("latency_ms", "latência acima de um dia; confira a unidade (milissegundos)")
	}
	return ms, nil
}

// CleanPushMessage deixa a causa informada pronta para circular.
//
// Caractere de controle e separador de linha ou parágrafo (U+2028,
// U+2029) viram espaço, porque uma quebra de linha permitiria forjar
// linhas extras no aviso. Formatação invisível (categoria Cf: inversão
// bidi, largura zero, BOM) sai sem deixar rastro, porque faz o texto
// exibido diferir do texto enviado — "falhou" lido de trás para a frente,
// uma menção escondida atrás de um espaço que não aparece. Byte inválido é
// substituído, porque alguns canais recusam a mensagem inteira; e o corte
// respeita caracteres, não bytes, pelo mesmo motivo.
//
// Neutralizar menções de chat (@everyone) não é papel daqui: cada canal
// tem a própria sintaxe, e quem a conhece é o notificador.
func CleanPushMessage(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), unicode.In(r, unicode.Zl, unicode.Zp):
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(raw, "�"))
	cleaned = strings.TrimSpace(cleaned)

	runes := []rune(cleaned)
	if len(runes) > MaxPushMessageLength {
		cleaned = strings.TrimSpace(string(runes[:MaxPushMessageLength]))
	}
	return cleaned
}
