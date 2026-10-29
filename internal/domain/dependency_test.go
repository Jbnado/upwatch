package domain_test

import (
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// rede monta um conjunto de monitores para as consultas de dependência.
type rede struct {
	monitores map[int64]domain.Monitor
	estados   map[int64]domain.MonitorState
}

func novaRede() *rede {
	return &rede{monitores: map[int64]domain.Monitor{}, estados: map[int64]domain.MonitorState{}}
}

func (r *rede) add(id int64, pai *int64, status domain.Status) {
	r.monitores[id] = domain.Monitor{ID: id, Name: "m", ParentID: pai, Enabled: true}
	r.estados[id] = domain.MonitorState{Status: status}
}

func (r *rede) lookup(id int64) (domain.Monitor, bool) {
	m, ok := r.monitores[id]
	return m, ok
}

func (r *rede) state(id int64) domain.MonitorState { return r.estados[id] }

func idp(id int64) *int64 { return &id }

// Recusar só a autorreferência deixava passar A → B → A, e um ciclo
// assim faria cada um calar o outro: os dois caem juntos, nenhum avisa.
func TestParentCycleRejectsLoopsOfAnyLength(t *testing.T) {
	r := novaRede()
	r.add(1, nil, domain.StatusUp)
	r.add(2, idp(1), domain.StatusUp)
	r.add(3, idp(2), domain.StatusUp)

	for _, tt := range []struct {
		nome  string
		id    int64
		pai   *int64
		ciclo bool
	}{
		{"sem pai", 1, nil, false},
		{"a si mesmo", 1, idp(1), true},
		{"ao filho", 1, idp(2), true},
		{"ao neto", 1, idp(3), true},
		{"a um irmão de fora da cadeia", 3, idp(1), false},
		{"monitor novo", 0, idp(3), false},
		{"pai inexistente", 1, idp(99), false},
	} {
		t.Run(tt.nome, func(t *testing.T) {
			if got := domain.ParentCycle(tt.id, tt.pai, r.lookup); got != tt.ciclo {
				t.Errorf("ParentCycle(%d, %v) = %v, want %v", tt.id, tt.pai, got, tt.ciclo)
			}
		})
	}
}

// Um ciclo que já estava no banco, acima do monitor e sem passar por
// ele, não pode travar a consulta num laço infinito.
func TestParentCycleSurvivesAnExistingLoopAbove(t *testing.T) {
	r := novaRede()
	r.add(1, idp(2), domain.StatusUp)
	r.add(2, idp(1), domain.StatusUp)
	r.add(3, nil, domain.StatusUp)

	if domain.ParentCycle(3, idp(1), r.lookup) {
		t.Error("ParentCycle reported a cycle through 3, which is not in the loop")
	}
}

// O pai em queda confirmada cala o filho. A queda do avô também: o pai,
// calado por ele, nunca chegaria a confirmar a própria queda, e o neto
// voltaria a avisar.
func TestDownAncestorFindsTheNearestConfirmedDown(t *testing.T) {
	r := novaRede()
	r.add(1, nil, domain.StatusDown)
	r.add(2, idp(1), domain.StatusUp)
	r.add(3, idp(2), domain.StatusUp)

	pai, ok := domain.DownAncestor(r.monitores[3], r.lookup, r.state)
	if !ok || pai.ID != 1 {
		t.Fatalf("DownAncestor = %d, %v; want the grandparent (1)", pai.ID, ok)
	}

	r.estados[2] = domain.MonitorState{Status: domain.StatusDown}
	pai, ok = domain.DownAncestor(r.monitores[3], r.lookup, r.state)
	if !ok || pai.ID != 2 {
		t.Errorf("DownAncestor = %d, %v; want the nearest one (2)", pai.ID, ok)
	}
}

// Pai sem medição não cala ninguém: um pai mal configurado, que nunca
// chega a medir, silenciaria os filhos para sempre. Pai pausado também
// não, porque o estado dele parou no tempo — mas o avô, se cair, ainda
// conta.
func TestDownAncestorIgnoresUnknownAndPausedParents(t *testing.T) {
	r := novaRede()
	r.add(1, nil, domain.StatusUnknown)
	r.add(2, idp(1), domain.StatusUp)
	if _, ok := domain.DownAncestor(r.monitores[2], r.lookup, r.state); ok {
		t.Error("an unknown parent silenced its child")
	}

	r.add(1, nil, domain.StatusDown)
	pausado := r.monitores[1]
	pausado.Enabled = false
	r.monitores[1] = pausado
	if _, ok := domain.DownAncestor(r.monitores[2], r.lookup, r.state); ok {
		t.Error("a paused parent frozen at down silenced its child")
	}

	r.add(0, nil, domain.StatusDown)
	pausado.ParentID = idp(0)
	r.monitores[1] = pausado
	if pai, ok := domain.DownAncestor(r.monitores[2], r.lookup, r.state); !ok || pai.ID != 0 {
		t.Errorf("DownAncestor = %d, %v; want the grandparent above the paused parent", pai.ID, ok)
	}
}

// Um ciclo gravado antes da validação existir não pode travar o motor.
func TestDownAncestorSurvivesALoop(t *testing.T) {
	r := novaRede()
	r.add(1, idp(2), domain.StatusUp)
	r.add(2, idp(1), domain.StatusUp)

	if _, ok := domain.DownAncestor(r.monitores[1], r.lookup, r.state); ok {
		t.Error("DownAncestor found a down ancestor in a loop of healthy monitors")
	}
}
