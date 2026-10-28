package domain_test

import (
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Ordem de gravidade.
//
// Quatro lugares escolhiam "o pior estado" — a lista de problemas, a
// página pública, a faixa do painel e o agregado —, cada um com a sua
// tabela de pesos. Concordavam por coincidência. A ordem agora é uma só,
// e a única divergência legítima, onde fica "sem medição", é um parâmetro
// explícito de quem pergunta.

var observados = []domain.Status{domain.StatusUp, domain.StatusDegraded, domain.StatusDown}

// Fora do ar pesa mais que degradado, que pesa mais que no ar — nas duas
// leituras.
func TestSeverityOrdersObservedStatesTheSameWayEverywhere(t *testing.T) {
	for _, u := range []domain.UnknownPlacement{domain.UnknownIsAbsence, domain.UnknownIsProblem} {
		for i := 1; i < len(observados); i++ {
			if domain.Severity(observados[i], u) <= domain.Severity(observados[i-1], u) {
				t.Errorf("Severity(%v) <= Severity(%v) com %v", observados[i], observados[i-1], u)
			}
		}
	}
}

// "Sem medição" como ausência fica abaixo de tudo: um bucket sem batida
// não pode esconder a batida que veio, e um monitor recém-criado não pode
// fazer a página pública inteira dizer que não sabe nada. Como problema
// fica acima de "no ar" e abaixo de qualquer falha: para quem opera, um
// alvo que parou de ser medido é algo a olhar, mas menos que uma queda.
func TestSeverityPlacesUnknownByTheQuestionAsked(t *testing.T) {
	sem := domain.StatusUnknown
	if domain.Severity(sem, domain.UnknownIsAbsence) >= domain.Severity(domain.StatusUp, domain.UnknownIsAbsence) {
		t.Error("como ausência, sem medição pesou tanto quanto no ar")
	}
	if domain.Severity(sem, domain.UnknownIsProblem) <= domain.Severity(domain.StatusUp, domain.UnknownIsProblem) ||
		domain.Severity(sem, domain.UnknownIsProblem) >= domain.Severity(domain.StatusDegraded, domain.UnknownIsProblem) {
		t.Error("como problema, sem medição precisa ficar entre no ar e degradado")
	}
}

// Quem escolhe o pior estado passa pela mesma função: a página pública e
// a lista de problemas discordam só sobre "sem medição", e por escolha.
func TestEveryWorstOfUsesTheSameOrder(t *testing.T) {
	todos := append([]domain.Status{domain.StatusUnknown}, observados...)
	for _, a := range todos {
		for _, b := range todos {
			par := []domain.Status{a, b}
			if got, want := domain.OverallStatus(par), domain.Worst(par, domain.UnknownIsAbsence); got != want {
				t.Errorf("OverallStatus(%v) = %v, Worst como ausência = %v", par, got, want)
			}
			if got, want := domain.WorstStatus(par), domain.Worst(par, domain.UnknownIsProblem); got != want {
				t.Errorf("WorstStatus(%v) = %v, Worst como problema = %v", par, got, want)
			}
			if domain.ProblemRank(a) != domain.Severity(a, domain.UnknownIsProblem) {
				t.Errorf("ProblemRank(%v) diverge de Severity", a)
			}
		}
	}
}
