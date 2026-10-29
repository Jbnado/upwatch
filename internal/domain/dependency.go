package domain

// MonitorLookup acha um monitor pelo id; ok falso quando ele não existe.
//
// Função, e não mapa nem store: o motor consulta o que tem em memória, a
// API o que leu do banco, e a regra de dependência é a mesma para os dois.
type MonitorLookup func(id int64) (Monitor, bool)

// ParentCycle informa se pôr parentID como pai de id fecharia um ciclo.
//
// A autorreferência é o ciclo mais curto, e já é recusada pelo Validate.
// Este pega os demais: A → B → A faria cada um calar o outro, e os dois
// cairiam juntos sem que nenhum avisasse.
//
// Um ciclo que já esteja no banco, acima do pai e sem passar por id, não
// é deste pedido: a subida para nele em vez de girar para sempre.
func ParentCycle(id int64, parentID *int64, lookup MonitorLookup) bool {
	if parentID == nil {
		return false
	}
	vistos := map[int64]bool{}
	for atual := *parentID; ; {
		if atual == id {
			return true
		}
		if vistos[atual] {
			return false
		}
		vistos[atual] = true

		m, ok := lookup(atual)
		if !ok || m.ParentID == nil {
			return false
		}
		atual = *m.ParentID
	}
}

// DownAncestor devolve o ancestral mais próximo em queda confirmada, que
// é quem cala o monitor.
//
// A subida passa do pai ao avô: o pai calado pelo avô nunca confirma a
// própria queda, e sem isso o neto voltaria a avisar justamente quando a
// causa está dois níveis acima.
//
// Dois ancestrais não calam ninguém:
//   - o sem medição (unknown), porque um pai mal configurado, que nunca
//     chega a medir, silenciaria os filhos para sempre — o mesmo princípio
//     da sentinela de rede, que só vale depois de ter funcionado uma vez;
//   - o pausado, porque o estado dele parou no tempo e ninguém o confere.
//     A subida continua acima dele.
func DownAncestor(m Monitor, lookup MonitorLookup, state func(id int64) MonitorState) (Monitor, bool) {
	vistos := map[int64]bool{m.ID: true}
	for p := m.ParentID; p != nil; {
		if vistos[*p] {
			return Monitor{}, false
		}
		vistos[*p] = true

		pai, ok := lookup(*p)
		if !ok {
			return Monitor{}, false
		}
		if pai.Enabled && state(pai.ID).Status == StatusDown {
			return pai, true
		}
		p = pai.ParentID
	}
	return Monitor{}, false
}
