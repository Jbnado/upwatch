package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Grupos de monitores.
//
// Etiqueta é rótulo solto, e vinha sendo usada para três coisas que pedem
// estrutura: organizar a tela, controlar quem vê o quê e decidir quem é
// avisado. Montar acesso sobre etiqueta seria frágil — quem acrescenta a
// etiqueta "retaguarda" num monitor concederia acesso a ele sem perceber.
// Grupo é entidade própria, e a etiqueta volta a ser só recorte.
//
// Dois níveis no máximo: Sistema → Ambiente ("RPA" → "prod") cobre o caso
// comum sem virar árvore de pastas, onde ninguém sabe em que profundidade
// procurar.

// maxGroupNameLength limita o nome. Nome de grupo vai em cartão e em
// cabeçalho; frase não cabe em nenhum dos dois.
const maxGroupNameLength = 60

// Group é um conjunto de monitores. Monitor pertence a um grupo folha ou a
// "Sem grupo".
type Group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`

	// ParentID nulo é um grupo de topo (um sistema). Preenchido, aponta o
	// sistema do ambiente.
	ParentID *int64 `json:"parent_id,omitempty"`

	// ReminderInterval vale para os monitores do grupo que não escolheram
	// o próprio. Nulo herda do sistema (ou da instalação); zero desliga.
	ReminderInterval *time.Duration `json:"reminder_interval,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes que não dependem dos outros grupos. A
// forma da árvore é conferida por GroupTree.CheckPlacement.
func (g Group) Validate() error {
	name := strings.TrimSpace(g.Name)
	if name == "" {
		return invalid("name", "não pode ser vazio")
	}
	if len(name) > maxGroupNameLength {
		return invalid("name", fmt.Sprintf("não pode passar de %d caracteres", maxGroupNameLength))
	}
	if err := ValidateSlug(g.Slug); err != nil {
		return err
	}
	if g.ParentID != nil && g.ID != 0 && *g.ParentID == g.ID {
		return invalid("parent_id", "um grupo não pode ser pai de si mesmo")
	}
	if err := ValidateReminderInterval(g.ReminderInterval); err != nil {
		return err
	}
	return nil
}

// acentos cobre o que aparece em nome de sistema digitado em português.
var acentos = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "ê", "e", "è", "e", "ë", "e",
	"í", "i", "î", "i", "ì", "i", "ï", "i",
	"ó", "o", "ô", "o", "õ", "o", "ò", "o", "ö", "o",
	"ú", "u", "û", "u", "ù", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

// Slugify deriva o slug do nome, para quem cadastra não precisar dizer a
// mesma coisa duas vezes.
//
// Tudo que não for letra ou número vira hífen, e hífens seguidos viram
// um: "RPA · prod" vira "rpa-prod", não "rpa---prod".
func Slugify(name string) string {
	s := acentos.Replace(strings.ToLower(strings.TrimSpace(name)))

	var b strings.Builder
	hifen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hifen = false
			continue
		}
		if !hifen && b.Len() > 0 {
			b.WriteByte('-')
			hifen = true
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if len(out) > maxSlugLength {
		out = strings.TrimSuffix(out[:maxSlugLength], "-")
	}
	return out
}

// GroupTree é a instalação inteira de grupos, montada uma vez por decisão.
//
// As perguntas sobre forma — quem é folha, qual o caminho, se um pai
// serve — dependem de todos os grupos, e por isso são feitas aqui e não
// espalhadas por quem as consome.
type GroupTree struct {
	byID     map[int64]Group
	children map[int64][]int64
}

// NewGroupTree indexa os grupos.
func NewGroupTree(groups []Group) GroupTree {
	t := GroupTree{
		byID:     make(map[int64]Group, len(groups)),
		children: map[int64][]int64{},
	}
	for _, g := range groups {
		t.byID[g.ID] = g
	}
	for _, g := range groups {
		if g.ParentID != nil {
			t.children[*g.ParentID] = append(t.children[*g.ParentID], g.ID)
		}
	}
	return t
}

// Get devolve o grupo, e se existe.
func (t GroupTree) Get(id int64) (Group, bool) {
	g, ok := t.byID[id]
	return g, ok
}

// Children devolve os ambientes de um sistema.
func (t GroupTree) Children(id int64) []int64 { return t.children[id] }

// Len é quantos grupos existem.
func (t GroupTree) Len() int { return len(t.byID) }

// All devolve os grupos em ordem de caminho, que é a ordem de leitura.
func (t GroupTree) All() []Group {
	out := make([]Group, 0, len(t.byID))
	for _, g := range t.byID {
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b Group) int {
		if c := strings.Compare(strings.ToLower(t.Path(a.ID)), strings.ToLower(t.Path(b.ID))); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return out
}

// IsLeaf informa se o grupo pode receber monitores.
//
// Só folha recebe monitor. Um sistema com ambientes e monitores soltos ao
// lado deles teria dois tipos de filho, e a pergunta "como está o RPA?"
// deixaria de ter uma resposta só.
func (t GroupTree) IsLeaf(id int64) bool {
	if _, ok := t.byID[id]; !ok {
		return false
	}
	return len(t.children[id]) == 0
}

// Path é o nome completo do grupo, como aparece na tela: "RPA / prod".
func (t GroupTree) Path(id int64) string {
	g, ok := t.byID[id]
	if !ok {
		return ""
	}
	if g.ParentID == nil {
		return g.Name
	}
	pai, ok := t.byID[*g.ParentID]
	if !ok {
		return g.Name
	}
	return pai.Name + " / " + g.Name
}

// CheckPlacement confere se o grupo cabe na árvore no lugar pedido.
//
// O pai precisa existir e ser de topo, e um grupo que já tem filhos não
// pode ganhar pai: as duas situações criariam um terceiro nível.
func (t GroupTree) CheckPlacement(g Group) error {
	if g.ParentID == nil {
		return nil
	}
	if g.ID != 0 && *g.ParentID == g.ID {
		return invalid("parent_id", "um grupo não pode ser pai de si mesmo")
	}
	pai, ok := t.byID[*g.ParentID]
	if !ok {
		return invalid("parent_id", "grupo pai não encontrado")
	}
	if pai.ParentID != nil {
		return invalid("parent_id", "grupos têm no máximo dois níveis; o pai precisa ser um grupo de topo")
	}
	if g.ID != 0 && len(t.children[g.ID]) > 0 {
		return invalid("parent_id", "um grupo com ambientes não pode ficar dentro de outro")
	}
	return nil
}
