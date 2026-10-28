package domain

import (
	"encoding/json"
	"slices"
)

// Escopo de acesso.
//
// O papel continua respondendo "essa pessoa pode mexer?"; o escopo
// responde "sobre o quê". São duas perguntas independentes, e um
// observador da Retaguarda e um administrador da Retaguarda diferem só
// na primeira.
//
// Toda decisão de alcance passa por Access. Ela é tomada uma vez, aqui, e
// os endpoints só perguntam — dois lugares decidindo a mesma coisa
// acabam divergindo, e em acesso a divergência é vazamento.

// Scope é o que a conta alcança: todos os grupos, ou uma lista deles.
//
// O valor zero é "todos os grupos". É o que toda conta tinha antes de
// existir escopo, e a migração não pode trancar ninguém para fora da
// própria instalação. Restringir é sempre um ato explícito.
type Scope struct {
	// Restricted falso alcança tudo, inclusive monitores sem grupo.
	Restricted bool
	// GroupIDs são os grupos concedidos quando Restricted. Conceder um
	// sistema concede os ambientes dele.
	GroupIDs []int64
}

// Validate recusa escopo restrito sem grupo nenhum.
//
// Uma conta assim não enxerga nada; é quase sempre engano de quem
// preencheu, e o erro precisa aparecer na hora, não quando a pessoa
// reclamar que a tela está vazia.
func (s Scope) Validate() error {
	if s.Restricted && len(s.GroupIDs) == 0 {
		return invalid("scope", "escolha ao menos um grupo, ou conceda todos")
	}
	return nil
}

// Normalize ordena e tira repetição, para a mesma concessão ter uma forma
// só no banco e na resposta.
func (s Scope) Normalize() Scope {
	if !s.Restricted {
		return Scope{}
	}
	ids := slices.Clone(s.GroupIDs)
	slices.Sort(ids)
	return Scope{Restricted: true, GroupIDs: slices.Compact(ids)}
}

type scopeJSON struct {
	AllGroups bool    `json:"all_groups"`
	GroupIDs  []int64 `json:"group_ids"`
}

// MarshalJSON diz o que a conta alcança, e não o contrário: quem lê a
// resposta pergunta "vê tudo?".
func (s Scope) MarshalJSON() ([]byte, error) {
	n := s.Normalize()
	ids := n.GroupIDs
	if ids == nil {
		ids = []int64{}
	}
	return json.Marshal(scopeJSON{AllGroups: !n.Restricted, GroupIDs: ids})
}

// UnmarshalJSON implementa json.Unmarshaler.
func (s *Scope) UnmarshalJSON(data []byte) error {
	var raw scopeJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = Scope{Restricted: !raw.AllGroups, GroupIDs: raw.GroupIDs}
	return nil
}

// Access é a decisão de alcance de uma conta, já resolvida contra os
// grupos que existem agora.
//
// Resolver contra a árvore atual, e não guardar a expansão, é o que faz
// um ambiente criado depois sob um sistema concedido entrar no escopo
// sem ninguém precisar atualizar a conta.
type Access struct {
	all     bool
	granted map[int64]struct{}
	known   map[int64]struct{}
}

// NewAccess resolve o escopo contra a árvore de grupos.
func NewAccess(s Scope, tree GroupTree) Access {
	acc := Access{
		all:     !s.Restricted,
		granted: map[int64]struct{}{},
		known:   make(map[int64]struct{}, tree.Len()),
	}
	for id := range tree.byID {
		acc.known[id] = struct{}{}
	}
	if acc.all {
		return acc
	}
	for _, id := range s.GroupIDs {
		// Grupo apagado some da concessão: um id órfão não pode valer
		// para outro grupo que um dia reaproveite o número.
		if _, ok := tree.byID[id]; !ok {
			continue
		}
		acc.granted[id] = struct{}{}
		for _, filho := range tree.children[id] {
			acc.granted[filho] = struct{}{}
		}
	}
	return acc
}

// All informa se a conta alcança tudo, inclusive o que não tem grupo.
func (a Access) All() bool { return a.all }

// Group informa se a conta alcança o grupo. Grupo inexistente nunca é
// alcançado, nem pelo escopo total.
func (a Access) Group(id int64) bool {
	if _, ok := a.known[id]; !ok {
		return false
	}
	if a.all {
		return true
	}
	_, ok := a.granted[id]
	return ok
}

// MonitorGroup informa se a conta alcança um monitor que está no grupo
// indicado (nulo é "Sem grupo").
//
// "Sem grupo" só é visível a quem alcança tudo: não é um grupo concedido
// a ninguém, e mostrá-lo a quem tem escopo faria todo monitor recém-criado
// sem grupo aparecer para todo mundo.
func (a Access) MonitorGroup(groupID *int64) bool {
	if groupID == nil {
		return a.all
	}
	return a.Group(*groupID)
}

// Monitor informa se a conta alcança o monitor.
func (a Access) Monitor(m Monitor) bool { return a.MonitorGroup(m.GroupID) }

// Grants informa se a conta pode conceder o escopo a outra.
//
// É a guarda contra escalonamento: quem tem escopo não cria nem altera
// conta com escopo maior que o dele, senão bastaria criar uma conta nova
// para sair do próprio escopo.
func (a Access) Grants(s Scope) bool {
	if !s.Restricted {
		return a.all
	}
	for _, id := range s.GroupIDs {
		if !a.Group(id) {
			return false
		}
	}
	return true
}

// GroupIDs devolve, ordenados, os grupos alcançados por um escopo
// restrito. Para o escopo total, nil — quem consulta usa All primeiro.
func (a Access) GroupIDs() []int64 {
	if a.all {
		return nil
	}
	out := make([]int64, 0, len(a.granted))
	for id := range a.granted {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// ProblemRank ordena os estados pela urgência para quem opera: a ordem de
// Severity com "sem medição" como problema (ver UnknownIsProblem).
func ProblemRank(s Status) int { return Severity(s, UnknownIsProblem) }

// WorstStatus devolve o estado mais urgente para quem opera. Sem estado
// nenhum, não há medição.
func WorstStatus(statuses []Status) Status { return Worst(statuses, UnknownIsProblem) }
