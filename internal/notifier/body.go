package notifier

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Marcadores são as variáveis que um mapeamento de corpo pode usar.
//
// Exportado porque a interface precisa mostrar a lista a quem escreve o
// mapeamento: um recurso cuja única documentação está no README vira um
// recurso que ninguém usa. Um teste garante que esta lista e os valores
// realmente produzidos não se separem.
func Marcadores() []string {
	nomes := make([]string, 0, len(marcadores))
	for nome := range marcadores {
		nomes = append(nomes, nome)
	}
	sort.Strings(nomes)
	return nomes
}

// marcadores mapeia cada nome ao valor que ele produz.
//
// O tipo importa: monitor_id e duration_seconds saem como número, e é o
// que permite entregar um corpo que passa numa validação de esquema do
// outro lado.
var marcadores = map[string]func(dados) any{
	"text":             func(d dados) any { return d.texto },
	"monitor":          func(d dados) any { return d.n.Monitor.Name },
	"monitor_id":       func(d dados) any { return d.n.Monitor.ID },
	"target":           func(d dados) any { return d.n.Monitor.Target },
	"status":           func(d dados) any { return d.n.Status() },
	"previous_status":  func(d dados) any { return d.n.Event.From.String() },
	"message":          func(d dados) any { return d.n.Message },
	"at":               func(d dados) any { return d.n.Event.At.Format(time.RFC3339Nano) },
	"duration_seconds": func(d dados) any { return int(d.n.Event.Duration.Seconds()) },
}

// dados é o que os marcadores enxergam.
//
// O texto vem pronto, e não é recalculado aqui, porque ele já passou pelo
// modelo de mensagem do canal — $text precisa entregar a mesma frase que
// o canal enviaria sem mapeamento.
type dados struct {
	n     Notification
	texto string
}

// bodyTemplate é o mapeamento de corpo declarado no canal.
//
// A substituição acontece sobre a estrutura JSON já decodificada, e o
// resultado volta a ser serializado por encoding/json. Montar o corpo por
// concatenação de texto seria mais simples e estaria errado: nome de
// monitor e causa de falha são texto livre, e uma aspa dentro deles
// produziria um corpo que o destino recusa — perdendo o aviso da queda
// por causa da própria queda.
type bodyTemplate struct{ raiz any }

// compileBody prepara o mapeamento, se houver.
//
// A validação acontece no cadastro para o erro aparecer ali, e não durante
// o incidente. Um marcador com erro de digitação descoberto às três da
// manhã é um aviso que não foi entregue.
func compileBody(raw json.RawMessage) (*bodyTemplate, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var raiz any
	if err := json.Unmarshal(raw, &raiz); err != nil {
		return nil, fmt.Errorf("notifier: mapeamento de corpo inválido: %w", err)
	}
	if err := conferir(raiz); err != nil {
		return nil, err
	}
	return &bodyTemplate{raiz: raiz}, nil
}

// render aplica o mapeamento aos dados do evento.
func (b *bodyTemplate) render(d dados) any { return substituir(b.raiz, d) }

// conferir recusa marcador desconhecido em qualquer profundidade.
func conferir(v any) error {
	switch t := v.(type) {
	case map[string]any:
		// As chaves ficam de fora de propósito: elas são o esquema que o
		// destino espera, e um esquema que muda de forma a cada evento não
		// é um esquema.
		for _, sub := range t {
			if err := conferir(sub); err != nil {
				return err
			}
		}
	case []any:
		for _, sub := range t {
			if err := conferir(sub); err != nil {
				return err
			}
		}
	case string:
		for _, s := range fatiar(t) {
			if s.marcador == "" {
				continue
			}
			if _, ok := marcadores[s.marcador]; !ok {
				return fmt.Errorf("notifier: marcador desconhecido $%s; disponíveis: %s",
					s.marcador, strings.Join(Marcadores(), ", "))
			}
		}
	}
	return nil
}

// substituir devolve a estrutura com os marcadores resolvidos.
func substituir(v any, d dados) any {
	switch t := v.(type) {
	case map[string]any:
		saida := make(map[string]any, len(t))
		for chave, sub := range t {
			saida[chave] = substituir(sub, d)
		}
		return saida

	case []any:
		saida := make([]any, len(t))
		for i, sub := range t {
			saida[i] = substituir(sub, d)
		}
		return saida

	case string:
		return expandir(t, d)

	default:
		// Número, booleano e null são a parte fixa do contrato com o
		// destino e passam intactos.
		return v
	}
}

// expandir resolve os marcadores de um texto.
//
// Um marcador sozinho entrega o valor com o tipo dele; no meio de um texto,
// compõe uma frase. A diferença existe porque "$duration_seconds" precisa
// chegar como número em quem valida esquema, e "caiu há $duration_seconds s"
// só pode ser texto.
func expandir(s string, d dados) any {
	partes := fatiar(s)

	if len(partes) == 1 && partes[0].marcador != "" {
		return valor(partes[0].marcador, d)
	}

	var b strings.Builder
	for _, p := range partes {
		if p.marcador == "" {
			b.WriteString(p.texto)
			continue
		}
		fmt.Fprintf(&b, "%v", valor(p.marcador, d))
	}
	return b.String()
}

func valor(nome string, d dados) any {
	f, ok := marcadores[nome]
	if !ok {
		// Inalcançável: o cadastro já recusou o desconhecido. Devolver o
		// literal é melhor que entrar em pânico durante um incidente.
		return "$" + nome
	}
	return f(d)
}

// segmento é um pedaço de texto literal ou um marcador.
type segmento struct {
	texto    string
	marcador string
}

// fatiar separa o texto em literais e marcadores.
//
// Aceita $nome e ${nome}; as chaves servem para quando o nome encosta em
// texto que continuaria o identificador. $$ escapa o cifrão, senão não
// haveria como enviar um literal.
func fatiar(s string) []segmento {
	var (
		partes []segmento
		buf    strings.Builder
	)

	solta := func() {
		if buf.Len() > 0 {
			partes = append(partes, segmento{texto: buf.String()})
			buf.Reset()
		}
	}

	for i := 0; i < len(s); {
		if s[i] != '$' {
			buf.WriteByte(s[i])
			i++
			continue
		}

		// $$ vira um cifrão e segue adiante sem abrir marcador.
		if i+1 < len(s) && s[i+1] == '$' {
			buf.WriteByte('$')
			i += 2
			continue
		}

		if i+1 < len(s) && s[i+1] == '{' {
			if fim := strings.IndexByte(s[i+2:], '}'); fim >= 0 {
				solta()
				partes = append(partes, segmento{marcador: s[i+2 : i+2+fim]})
				i += 2 + fim + 1
				continue
			}
		}

		fim := i + 1
		for fim < len(s) && identificador(s[fim]) {
			fim++
		}
		if fim == i+1 {
			// Cifrão solto, sem nome atrás: é literal.
			buf.WriteByte('$')
			i++
			continue
		}

		solta()
		partes = append(partes, segmento{marcador: s[i+1 : fim]})
		i = fim
	}

	solta()
	if len(partes) == 0 {
		partes = append(partes, segmento{})
	}
	return partes
}

func identificador(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}
