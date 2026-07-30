package domain_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// A página pública é a única superfície do UpWatch que qualquer pessoa
// alcança sem credencial. O que se testa aqui é sobretudo o que ela NÃO
// revela: o endereço do alvo e a causa da falha descrevem a topologia
// interna, e quem tem o link não precisa disso para saber se o serviço
// está no ar.

func TestStatusPageValidateAceitaPaginaCompleta(t *testing.T) {
	p := domain.StatusPage{Slug: "estado-da-plataforma", Title: "Estado da plataforma"}

	if err := p.Validate(); err != nil {
		t.Fatalf("página válida reprovada: %v", err)
	}
}

func TestStatusPageValidateExigeTitulo(t *testing.T) {
	p := domain.StatusPage{Slug: "estado", Title: "   "}

	err := p.Validate()
	if err == nil {
		t.Fatal("página sem título passou pela validação")
	}
	assertCampo(t, err, "title")
}

func TestStatusPageValidateRecusaSlugPerigoso(t *testing.T) {
	// O slug entra numa URL. Barra e ponto-ponto viram travessia de
	// caminho; espaço e acento viram sequência de escape que ninguém
	// consegue digitar de memória nem colar num chat sem quebrar.
	casos := map[string]string{
		"vazio":                "",
		"só espaço":            "   ",
		"com barra":            "estado/interno",
		"travessia":            "..",
		"travessia disfarçada": "a/../b",
		"com espaço":           "estado da plataforma",
		"com acento":           "estado-da-plataforma-são-paulo",
		"maiúscula":            "Estado",
		"hífen no início":      "-estado",
		"hífen no fim":         "estado-",
		"hífen duplo":          "estado--interno",
		"sublinhado":           "estado_interno",
		"ponto":                "estado.interno",
		"porcentagem":          "estado%2f",
		"longo demais":         strings.Repeat("a", 65),
	}

	for nome, slug := range casos {
		t.Run(nome, func(t *testing.T) {
			p := domain.StatusPage{Slug: slug, Title: "Estado"}

			err := p.Validate()
			if err == nil {
				t.Fatalf("slug %q foi aceito", slug)
			}
			assertCampo(t, err, "slug")
		})
	}
}

func TestStatusPageValidateAceitaSlugsUsuais(t *testing.T) {
	for _, slug := range []string{"estado", "api", "estado-da-plataforma", "v2", "a", "loja-br-2"} {
		t.Run(slug, func(t *testing.T) {
			p := domain.StatusPage{Slug: slug, Title: "Estado"}

			if err := p.Validate(); err != nil {
				t.Fatalf("slug %q reprovado: %v", slug, err)
			}
		})
	}
}

func TestPublicMonitorNaoRevelaEndereco(t *testing.T) {
	// O teste com dentes: se alguém trocar PublicMonitor por domain.Monitor
	// filtrado, ou acrescentar o campo "por conveniência", isto quebra.
	m := domain.PublicMonitor{
		Name:   "api-de-producao",
		Status: domain.StatusDown,
	}

	bruto, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("serializando: %v", err)
	}

	for _, proibido := range []string{"target", "cause", "config", "message", "parent"} {
		if strings.Contains(string(bruto), proibido) {
			t.Errorf("JSON público contém %q: %s", proibido, bruto)
		}
	}
}

func TestOverallStatusQualquerQuedaDominaOPainel(t *testing.T) {
	// Uma queda entre cinquenta alvos saudáveis não é "operacional": a
	// pessoa que abriu a página quer saber justamente do que caiu.
	got := domain.OverallStatus([]domain.Status{
		domain.StatusUp, domain.StatusUp, domain.StatusDown, domain.StatusDegraded,
	})

	if got != domain.StatusDown {
		t.Fatalf("esperava down, veio %s", got)
	}
}

func TestOverallStatusLentidaoAparecePorCimaDeSaudavel(t *testing.T) {
	got := domain.OverallStatus([]domain.Status{domain.StatusUp, domain.StatusDegraded})

	if got != domain.StatusDegraded {
		t.Fatalf("esperava degraded, veio %s", got)
	}
}

func TestOverallStatusAlvoSemMedicaoNaoDerrubaOResto(t *testing.T) {
	// Um monitor push recém-criado não pode fazer a página inteira dizer
	// "sem medição" enquanto o resto responde. Ele aparece como sem
	// medição na própria linha, e o topo continua contando a verdade.
	got := domain.OverallStatus([]domain.Status{domain.StatusUnknown, domain.StatusUp})

	if got != domain.StatusUp {
		t.Fatalf("esperava up, veio %s", got)
	}
}

func TestOverallStatusSemNadaMedidoEhSemMedicao(t *testing.T) {
	for nome, entrada := range map[string][]domain.Status{
		"lista vazia":  {},
		"tudo unknown": {domain.StatusUnknown, domain.StatusUnknown},
	} {
		t.Run(nome, func(t *testing.T) {
			if got := domain.OverallStatus(entrada); got != domain.StatusUnknown {
				t.Fatalf("esperava unknown, veio %s", got)
			}
		})
	}
}

func assertCampo(t *testing.T, err error, campo string) {
	t.Helper()

	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("erro não é *ValidationError: %T", err)
	}
	if ve.Field != campo {
		t.Fatalf("campo inválido: esperava %q, veio %q", campo, ve.Field)
	}
}
