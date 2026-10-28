package domain

// Ordem de gravidade dos estados.
//
// Tudo que escolhe "o pior estado" passa por aqui: a lista de problemas e
// o cartão de grupo, a página pública, a faixa do painel e o agregado.
// Antes cada um tinha a sua tabela de pesos e concordava por coincidência;
// a primeira mudança numa delas faria a página pública dizer uma coisa e
// o painel outra.
//
// Fora do ar pesa mais que degradado, que pesa mais que no ar, sempre. A
// única divergência legítima é onde fica "sem medição", e ela depende da
// pergunta — por isso é um parâmetro de quem pergunta, e não uma segunda
// tabela.

// UnknownPlacement diz onde "sem medição" entra na ordem.
type UnknownPlacement int

const (
	// UnknownIsAbsence trata "sem medição" como falta de observação, abaixo
	// de qualquer estado observado. É a leitura da faixa e do agregado (o
	// bucket sem batida não pode esconder a batida que veio) e da página
	// pública (um monitor push recém-criado não pode fazer o topo inteiro
	// dizer que não sabe nada enquanto o resto responde).
	UnknownIsAbsence UnknownPlacement = iota
	// UnknownIsProblem trata "sem medição" como problema, acima de no ar e
	// abaixo de qualquer falha observada. É a leitura de quem opera: um
	// alvo que parou de ser medido é algo a olhar, mas menos urgente que
	// uma queda.
	UnknownIsProblem
)

// Severity devolve o peso do estado: maior é mais grave.
func Severity(s Status, unknown UnknownPlacement) int {
	switch s {
	case StatusDown:
		return 4
	case StatusDegraded:
		return 3
	case StatusUp:
		return 1
	default:
		if unknown == UnknownIsProblem {
			return 2
		}
		return 0
	}
}

// Worse devolve o mais grave entre dois estados; no empate, o primeiro.
func Worse(a, b Status, unknown UnknownPlacement) Status {
	if Severity(b, unknown) > Severity(a, unknown) {
		return b
	}
	return a
}

// Worst devolve o mais grave da lista. Lista vazia é "sem medição": não
// há o que afirmar.
func Worst(statuses []Status, unknown UnknownPlacement) Status {
	pior := StatusUnknown
	for i, s := range statuses {
		if i == 0 {
			pior = s
			continue
		}
		pior = Worse(pior, s, unknown)
	}
	return pior
}
