package domain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Escopo de acesso.
//
// O papel diz o que a conta pode fazer; o escopo diz sobre quais grupos.
// Toda decisão de "esta conta alcança este monitor?" passa por Access, e
// é por isso que as regras são testadas aqui e não em cada endpoint.

func restrito(ids ...int64) domain.Scope {
	return domain.Scope{Restricted: true, GroupIDs: ids}
}

func TestEscopoZeroAlcancaTudo(t *testing.T) {
	// O valor zero é "todos os grupos" porque é o que toda conta já tinha
	// antes de existir escopo: a migração não pode trancar ninguém para
	// fora da própria instalação.
	acc := domain.NewAccess(domain.Scope{}, arvore())

	if !acc.All() {
		t.Fatal("escopo zero deveria alcançar tudo")
	}
	if !acc.Monitor(domain.Monitor{}) {
		t.Error("escopo total precisa enxergar monitor sem grupo")
	}
	if !acc.Monitor(domain.Monitor{GroupID: ptr(int64(5))}) {
		t.Error("escopo total precisa enxergar qualquer grupo")
	}
}

func TestAcessoAoPaiValeParaOsFilhos(t *testing.T) {
	// Dar acesso ao Sistema dá acesso aos ambientes dele, inclusive os
	// criados depois: a concessão é sobre o sistema, não sobre a lista de
	// ambientes que existia no dia.
	acc := domain.NewAccess(restrito(1), arvore())

	for _, id := range []int64{1, 2, 3} {
		if !acc.Group(id) {
			t.Errorf("acesso a RPA não alcançou o grupo %d", id)
		}
	}
	for _, id := range []int64{4, 5, 6} {
		if acc.Group(id) {
			t.Errorf("acesso a RPA vazou para o grupo %d", id)
		}
	}
}

func TestAcessoAoFilhoNaoSobeParaOPai(t *testing.T) {
	// Quem cuida da produção da Retaguarda não ganha a homologação dela
	// por tabela.
	acc := domain.NewAccess(restrito(5), arvore())

	if acc.Group(4) {
		t.Error("acesso ao ambiente subiu para o sistema")
	}
	if !acc.Monitor(domain.Monitor{GroupID: ptr(int64(5))}) {
		t.Error("monitor do ambiente concedido ficou de fora")
	}
}

func TestEscopoRestritoNaoVeSemGrupo(t *testing.T) {
	// "Sem grupo" não é um grupo concedido a ninguém. Deixá-lo visível a
	// quem tem escopo faria todo monitor recém-criado sem grupo aparecer
	// para todo mundo.
	acc := domain.NewAccess(restrito(1), arvore())

	if acc.Monitor(domain.Monitor{}) {
		t.Error("escopo restrito enxergou monitor sem grupo")
	}
}

func TestEscopoComGrupoInexistenteNaoConcedeNada(t *testing.T) {
	// Um grupo apagado some da concessão; o id órfão não pode valer para
	// um grupo criado depois com o mesmo número.
	acc := domain.NewAccess(restrito(99), arvore())

	if acc.Group(99) {
		t.Error("grupo inexistente foi concedido")
	}
}

func TestGrantsImpedeEscalonamento(t *testing.T) {
	// Quem tem escopo não cria conta com escopo maior que o dele; senão
	// bastaria criar uma conta nova para sair do próprio escopo.
	rpa := domain.NewAccess(restrito(1), arvore())

	if rpa.Grants(domain.Scope{}) {
		t.Error("escopo restrito concedeu todos os grupos")
	}
	if rpa.Grants(restrito(1, 4)) {
		t.Error("escopo RPA concedeu Retaguarda")
	}
	if !rpa.Grants(restrito(2)) {
		t.Error("escopo RPA deveria conceder RPA / prod")
	}
	if !rpa.Grants(restrito(1)) {
		t.Error("escopo RPA deveria conceder o próprio RPA")
	}

	prod := domain.NewAccess(restrito(2), arvore())
	if prod.Grants(restrito(1)) {
		t.Error("quem só tem RPA / prod concedeu RPA inteiro")
	}

	total := domain.NewAccess(domain.Scope{}, arvore())
	if !total.Grants(domain.Scope{}) || !total.Grants(restrito(4)) {
		t.Error("escopo total deveria conceder qualquer escopo")
	}
	if total.Grants(restrito(99)) {
		t.Error("nem o escopo total concede grupo inexistente")
	}
}

func TestScopeValidateExigeGrupoQuandoRestrito(t *testing.T) {
	// Escopo restrito sem grupo é uma conta que não enxerga nada. É quase
	// sempre engano de quem preencheu, e o erro precisa aparecer na hora.
	if err := restrito().Validate(); err == nil {
		t.Error("escopo restrito sem grupo foi aceito")
	}
	if err := (domain.Scope{}).Validate(); err != nil {
		t.Errorf("escopo total recusado: %v", err)
	}
}

func TestScopeJSON(t *testing.T) {
	// No JSON o campo diz o que a conta alcança ("all_groups"), e não o
	// contrário: quem lê a resposta pergunta "vê tudo?", não "é restrita?".
	b, err := json.Marshal(restrito(2, 1, 2))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"all_groups":false,"group_ids":[1,2]}` {
		t.Errorf("Marshal = %s", got)
	}

	var s domain.Scope
	if err := json.Unmarshal([]byte(`{"all_groups":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Restricted {
		t.Error("all_groups=true virou restrito")
	}
}

func TestUserJSONMostraEscopo(t *testing.T) {
	u := domain.User{ID: 1, Username: "ana", Role: domain.RoleViewer, Scope: restrito(4)}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"scope":{"all_groups":false,"group_ids":[4]}`) {
		t.Errorf("escopo não saiu no JSON da conta: %s", b)
	}
}

func TestProblemRankOrdena(t *testing.T) {
	// A ordem é a da tela inicial: fora do ar, degradado, sem medição e
	// por fim no ar. "Sem medição" fica acima de "no ar" aqui, ao
	// contrário da página pública, porque quem opera precisa saber que
	// um alvo parou de ser medido.
	ordem := []domain.Status{domain.StatusDown, domain.StatusDegraded, domain.StatusUnknown, domain.StatusUp}
	for i := 1; i < len(ordem); i++ {
		if domain.ProblemRank(ordem[i-1]) <= domain.ProblemRank(ordem[i]) {
			t.Errorf("%s deveria pesar mais que %s", ordem[i-1], ordem[i])
		}
	}
	if domain.WorstStatus([]domain.Status{domain.StatusUp, domain.StatusUnknown, domain.StatusUp}) != domain.StatusUnknown {
		t.Error("pior estado ignorou o sem medição")
	}
	if domain.WorstStatus(nil) != domain.StatusUnknown {
		t.Error("grupo vazio não tem medição")
	}
}
