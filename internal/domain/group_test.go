package domain_test

import (
	"errors"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Grupos.
//
// Grupo é a base da organização, do acesso e do aviso. Etiqueta continua
// existindo como recorte transversal, mas quem decide quem vê o quê é o
// grupo — por isso as regras de forma da árvore moram aqui, num lugar só.

// arvore monta a instalação de referência da issue: dois sistemas, cada um
// com produção e homologação.
func arvore() domain.GroupTree {
	return domain.NewGroupTree([]domain.Group{
		{ID: 1, Name: "RPA", Slug: "rpa"},
		{ID: 2, Name: "prod", Slug: "prod", ParentID: ptr(int64(1))},
		{ID: 3, Name: "hml", Slug: "hml", ParentID: ptr(int64(1))},
		{ID: 4, Name: "Retaguarda", Slug: "retaguarda"},
		{ID: 5, Name: "prod", Slug: "prod", ParentID: ptr(int64(4))},
		{ID: 6, Name: "Avulso", Slug: "avulso"},
	})
}

func TestGroupValidateExigeNomeESlug(t *testing.T) {
	casos := map[string]domain.Group{
		"nome vazio":     {Name: "  ", Slug: "x"},
		"slug com barra": {Name: "RPA", Slug: "rpa/prod"},
		"slug vazio":     {Name: "RPA", Slug: ""},
	}
	for nome, g := range casos {
		t.Run(nome, func(t *testing.T) {
			var verr *domain.ValidationError
			if err := g.Validate(); !errors.As(err, &verr) {
				t.Fatalf("grupo inválido passou: %v", err)
			}
		})
	}
}

func TestGroupValidateRecusaSerPaiDeSiMesmo(t *testing.T) {
	// Um ciclo de um só elemento faria o caminho "RPA / RPA / RPA..."
	// nunca terminar.
	g := domain.Group{ID: 7, Name: "RPA", Slug: "rpa", ParentID: ptr(int64(7))}
	if err := g.Validate(); err == nil {
		t.Fatal("grupo pai de si mesmo foi aceito")
	}
}

func TestSlugifyDerivaDoNome(t *testing.T) {
	// O slug é o nome em forma de endereço. Derivá-lo evita pedir duas
	// vezes a mesma informação a quem cadastra.
	casos := map[string]string{
		"Retaguarda":      "retaguarda",
		"Produção":        "producao",
		"  Homologação  ": "homologacao",
		"RPA · prod":      "rpa-prod",
	}
	for entrada, quer := range casos {
		if got := domain.Slugify(entrada); got != quer {
			t.Errorf("Slugify(%q) = %q, want %q", entrada, got, quer)
		}
	}
}

func TestGroupTreeCaminho(t *testing.T) {
	// O nome do monitor é curto dentro do grupo ("bff"); o caminho é o que
	// diferencia o bff de produção do de homologação na tela.
	tree := arvore()
	if got := tree.Path(2); got != "RPA / prod" {
		t.Errorf("Path(2) = %q", got)
	}
	if got := tree.Path(4); got != "Retaguarda" {
		t.Errorf("Path(4) = %q", got)
	}
}

func TestGroupTreeFolha(t *testing.T) {
	tree := arvore()
	if tree.IsLeaf(1) {
		t.Error("RPA tem ambientes e não é folha")
	}
	if !tree.IsLeaf(2) || !tree.IsLeaf(6) {
		t.Error("ambiente e grupo de topo sem filhos são folhas")
	}
	if tree.IsLeaf(99) {
		t.Error("grupo inexistente não pode ser folha")
	}
}

func TestGroupTreeRecusaTerceiroNivel(t *testing.T) {
	// Dois níveis cobrem Sistema → Ambiente sem virar árvore de pastas,
	// onde ninguém sabe em que profundidade procurar.
	tree := arvore()

	if err := tree.CheckPlacement(domain.Group{Name: "x", Slug: "x", ParentID: ptr(int64(2))}); err == nil {
		t.Error("filho de um ambiente (terceiro nível) foi aceito")
	}
	// Dar pai a um grupo que já tem filhos empurraria os filhos para o
	// terceiro nível.
	if err := tree.CheckPlacement(domain.Group{ID: 1, Name: "RPA", Slug: "rpa", ParentID: ptr(int64(4))}); err == nil {
		t.Error("grupo com filhos virou filho de outro")
	}
	if err := tree.CheckPlacement(domain.Group{Name: "x", Slug: "x", ParentID: ptr(int64(99))}); err == nil {
		t.Error("pai inexistente foi aceito")
	}
	if err := tree.CheckPlacement(domain.Group{Name: "dr", Slug: "dr", ParentID: ptr(int64(1))}); err != nil {
		t.Errorf("ambiente novo sob sistema foi recusado: %v", err)
	}
}
