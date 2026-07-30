package domain_test

import (
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Etiquetas de monitor.
//
// Servem para separar ambientes — homolog, produção — e times. São
// digitadas à mão, uma por vez, em momentos diferentes, e é justamente
// por isso que precisam de normalização: sem ela, "Produção", "produção"
// e "produção " viram três grupos para a mesma coisa, e a pessoa só
// descobre quando o painel mostra o mesmo nome duas vezes.

func TestNormalizeTagsUnificaCaixaEEspaco(t *testing.T) {
	got := domain.NormalizeTags([]string{"Produção", "  produção  ", "PRODUÇÃO"})

	if len(got) != 1 {
		t.Fatalf("esperava uma etiqueta, vieram %d: %v", len(got), got)
	}
	if got[0] != "produção" {
		t.Fatalf("normalização divergiu: %q", got[0])
	}
}

func TestNormalizeTagsDescartaVazias(t *testing.T) {
	// O campo da interface separa por vírgula; "prod,,homolog" é o que
	// sai de um dedo escorregando.
	got := domain.NormalizeTags([]string{"prod", "", "   ", "homolog"})

	if len(got) != 2 {
		t.Fatalf("esperava duas etiquetas, vieram %d: %v", len(got), got)
	}
}

func TestNormalizeTagsOrdenaParaExibicaoEstavel(t *testing.T) {
	// A ordem de digitação não é a ordem de leitura. Sem ordenar, o mesmo
	// conjunto apareceria em ordens diferentes conforme quem cadastrou.
	got := domain.NormalizeTags([]string{"web", "api", "banco"})

	if strings.Join(got, ",") != "api,banco,web" {
		t.Fatalf("ordem divergiu: %v", got)
	}
}

func TestNormalizeTagsPreservaNil(t *testing.T) {
	if got := domain.NormalizeTags(nil); got != nil {
		t.Fatalf("nil virou %v", got)
	}
	if got := domain.NormalizeTags([]string{"  "}); len(got) != 0 {
		t.Fatalf("só espaço virou %v", got)
	}
}

func TestValidateAceitaEtiquetasUsuais(t *testing.T) {
	m := monitorComTags("produção", "time-plataforma", "api_v2", "br")

	if err := m.Validate(); err != nil {
		t.Fatalf("etiquetas usuais reprovadas: %v", err)
	}
}

func TestValidateAceitaCuringaDeLike(t *testing.T) {
	// "%" e "_" são caracteres legítimos numa etiqueta, e "api_v2" é o
	// caso comum. Proibi-los aqui seria contorcer o domínio por causa de
	// uma consulta que interpola o valor num LIKE — o defeito mora no
	// store, e é lá que a conformidade cobre o escape.
	for _, tag := range []string{"api_v2", "100%", "a_b"} {
		t.Run(tag, func(t *testing.T) {
			if err := monitorComTags(tag).Validate(); err != nil {
				t.Fatalf("etiqueta %q reprovada: %v", tag, err)
			}
		})
	}
}

func TestValidateRecusaAspas(t *testing.T) {
	// As etiquetas são guardadas como array JSON e buscadas por
	// substring entre aspas; uma aspa dentro do valor quebra a
	// delimitação e faz a busca casar o que não devia.
	err := monitorComTags(`pro"d`).Validate()

	if err == nil {
		t.Fatal("etiqueta com aspas foi aceita")
	}
	assertCampo(t, err, "tags")
}

func TestValidateRecusaEtiquetaLonga(t *testing.T) {
	err := monitorComTags(strings.Repeat("a", 41)).Validate()

	if err == nil {
		t.Fatal("etiqueta longa demais foi aceita")
	}
	assertCampo(t, err, "tags")
}

func TestValidateLimitaAQuantidade(t *testing.T) {
	// Vinte etiquetas num alvo não organizam nada; só enchem a linha do
	// painel e a lista de filtros.
	muitas := make([]string, 0, 11)
	for i := range 11 {
		muitas = append(muitas, string(rune('a'+i)))
	}

	err := monitorComTags(muitas...).Validate()

	if err == nil {
		t.Fatal("etiquetas demais foram aceitas")
	}
	assertCampo(t, err, "tags")
}

func monitorComTags(tags ...string) domain.Monitor {
	return domain.Monitor{
		Name:                  "api",
		Type:                  domain.MonitorHTTP,
		Target:                "https://exemplo.com",
		Interval:              60_000_000_000,
		Timeout:               10_000_000_000,
		ConfirmationThreshold: 2,
		Tags:                  tags,
	}
}
