package notifier

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/template"
	"text/template/parse"
)

// Limites do modelo de mensagem do canal.
//
// A execução de um text/template não pode ser interrompida: um laço que
// começou só para quando acaba, e um modelo como {{range 1000000000}}
// prenderia a requisição do cadastro e, depois, o despachante no meio do
// incidente. Em vez de um prazo que não teria como ser cumprido, o
// trabalho é limitado antes de rodar — o que o modelo pode fazer — e a
// saída é limitada enquanto roda.
const (
	// maxTemplateLen limita o texto do modelo. Uma frase de aviso com
	// folga para condicionais cabe muito antes disso.
	maxTemplateLen = 8 << 10
	// maxTemplateOutput limita o que um modelo produz, e também cada
	// texto intermediário montado com print, printf, html, js ou
	// urlquery: sem isso, {{$a = print $a $a}} repetido dobra a memória a
	// cada linha. Nenhum destino aceita uma mensagem desse tamanho.
	maxTemplateOutput = 64 << 10
	// maxPrintfWidth limita largura e precisão no printf: "%01000000d"
	// monta um megabyte a partir de seis caracteres.
	maxPrintfWidth = 1000
)

// errSaidaGrande é o modelo que passou do tamanho permitido.
var errSaidaGrande = fmt.Errorf("o modelo produziu mais de %d KiB", maxTemplateOutput>>10)

// saidaLimitada é o destino da execução: recusa a escrita que passaria do
// limite, e a execução para no primeiro erro.
type saidaLimitada struct {
	w     io.Writer
	resta int
}

func (s *saidaLimitada) Write(p []byte) (int, error) {
	if len(p) > s.resta {
		return 0, errSaidaGrande
	}
	s.resta -= len(p)
	return s.w.Write(p)
}

// funcoesLimitadas troca as funções embutidas que montam texto por
// versões que recusam resultado acima do limite.
//
// O valor de uma variável ou de um argumento não passa pela saída, então o
// limite dela não o alcança; é aqui que o crescimento sem escrita para.
var funcoesLimitadas = template.FuncMap{
	"print":    func(args ...any) (string, error) { return textoLimitado(fmt.Sprint, args) },
	"println":  func(args ...any) (string, error) { return textoLimitado(fmt.Sprintln, args) },
	"printf":   printfLimitado,
	"html":     func(args ...any) (string, error) { return textoLimitado(template.HTMLEscaper, args) },
	"js":       func(args ...any) (string, error) { return textoLimitado(template.JSEscaper, args) },
	"urlquery": func(args ...any) (string, error) { return textoLimitado(template.URLQueryEscaper, args) },
}

// textoLimitado aplica f se a entrada couber, e confere a saída.
//
// A entrada é conferida antes porque o escape pode multiplicá-la: o js
// dobra cada barra invertida, e aplicado sobre si mesmo cresce em
// progressão geométrica.
func textoLimitado(f func(...any) string, args []any) (string, error) {
	if tamanhoTexto(args) > maxTemplateOutput {
		return "", errSaidaGrande
	}
	s := f(args...)
	if len(s) > maxTemplateOutput {
		return "", errSaidaGrande
	}
	return s, nil
}

func printfLimitado(formato string, args ...any) (string, error) {
	if err := conferirFormato(formato); err != nil {
		return "", err
	}
	return textoLimitado(func(a ...any) string { return fmt.Sprintf(formato, a...) }, args)
}

// tamanhoTexto soma o tamanho dos argumentos de texto. Os demais tipos ao
// alcance do modelo — números, a hora, as etiquetas — saem curtos.
func tamanhoTexto(args []any) int {
	total := 0
	for _, a := range args {
		if s, ok := a.(string); ok {
			total += len(s)
		}
	}
	return total
}

// conferirFormato recusa largura ou precisão grande, e a que vem de
// argumento (*), que escaparia da conferência.
func conferirFormato(formato string) error {
	numero, diretiva := 0, false
	for i := 0; i < len(formato); i++ {
		c := formato[i]
		switch {
		case !diretiva:
			diretiva, numero = c == '%', 0
		case c == '*':
			return errors.New("printf com largura ou precisão vinda de argumento (*) não é aceito")
		case c >= '0' && c <= '9':
			numero = numero*10 + int(c-'0')
			if numero > maxPrintfWidth {
				return fmt.Errorf("printf com largura ou precisão acima de %d não é aceito", maxPrintfWidth)
			}
		case strings.IndexByte(".+-# []", c) >= 0:
			numero = 0
		default:
			// O verbo encerra a diretiva; o segundo % de "%%" também.
			diretiva = false
		}
	}
	return nil
}

// conferirArvore recusa no cadastro o que deixaria o trabalho do modelo
// sem limite.
//
//   - range só sobre .Monitor.Tags, que tem no máximo dez itens, e sem
//     range dentro de range. Desde o Go 1.22 o range aceita um inteiro, e
//     {{range 1000000000}} — ou {{range .Monitor.ID}}, ou um inteiro
//     guardado numa variável — roda bilhões de voltas sem escrever nada,
//     então o limite da saída não o pararia. As etiquetas são a única
//     lista ao alcance do modelo; não há o que mais percorrer.
//   - sem define, template e block. Com eles, um modelo chama a si mesmo
//     duas vezes sobre um texto encurtado com slice, e o número de
//     chamadas dobra a cada caractere.
func conferirArvore(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errors.New("define e block não são aceitos no modelo")
	}
	if t.Tree == nil || t.Root == nil {
		return nil
	}
	return conferirLista(t.Root, 0)
}

func conferirLista(l *parse.ListNode, ranges int) error {
	if l == nil {
		return nil
	}
	for _, n := range l.Nodes {
		if err := conferirNo(n, ranges); err != nil {
			return err
		}
	}
	return nil
}

func conferirNo(n parse.Node, ranges int) error {
	switch n := n.(type) {
	case *parse.TemplateNode:
		return errors.New("template e block não são aceitos no modelo")
	case *parse.RangeNode:
		if ranges > 0 {
			return errors.New("range dentro de range não é aceito no modelo")
		}
		if !percorreEtiquetas(n.Pipe) {
			return errors.New("range só é aceito sobre .Monitor.Tags")
		}
		return conferirRamos(&n.BranchNode, ranges+1)
	case *parse.IfNode:
		return conferirRamos(&n.BranchNode, ranges)
	case *parse.WithNode:
		return conferirRamos(&n.BranchNode, ranges)
	case *parse.ListNode:
		return conferirLista(n, ranges)
	}
	return nil
}

// conferirRamos confere o corpo e o else. O else do range roda quando a
// lista está vazia, fora do laço, mas a regra é a mesma por simplicidade.
func conferirRamos(b *parse.BranchNode, ranges int) error {
	if err := conferirLista(b.List, ranges); err != nil {
		return err
	}
	return conferirLista(b.ElseList, ranges)
}

// percorreEtiquetas diz se o range percorre as etiquetas do monitor:
// .Monitor.Tags, $.Monitor.Tags, .Tags dentro de {{with .Monitor}} ou
// $m.Tags. O nome é o que basta conferir, porque Tags só existe no
// monitor, e é uma lista de texto.
func percorreEtiquetas(p *parse.PipeNode) bool {
	if p == nil || len(p.Cmds) != 1 || len(p.Cmds[0].Args) != 1 {
		return false
	}
	var ident []string
	switch a := p.Cmds[0].Args[0].(type) {
	case *parse.FieldNode:
		ident = a.Ident
	case *parse.VariableNode:
		// Só a variável, sem campo, pode guardar qualquer coisa.
		if len(a.Ident) < 2 {
			return false
		}
		ident = a.Ident
	default:
		return false
	}
	return ident[len(ident)-1] == "Tags"
}

// executarLimitado roda o modelo com a saída limitada.
func executarLimitado(t *template.Template, w io.Writer, dado any) error {
	return t.Execute(&saidaLimitada{w: w, resta: maxTemplateOutput}, dado)
}

// conferirTamanho recusa o modelo comprido demais antes de compilá-lo.
func conferirTamanho(raw string) error {
	if len(raw) > maxTemplateLen {
		return fmt.Errorf("o modelo passa de %d KiB", maxTemplateLen>>10)
	}
	return nil
}
