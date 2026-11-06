package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

func monitorVálido(nome string) domain.Monitor {
	return domain.Monitor{
		Name: nome, Type: domain.MonitorHTTP, Target: "https://exemplo.com",
		Interval: time.Minute, Timeout: 5 * time.Second, ConfirmationThreshold: 1,
	}
}

// GroupTree.Path monta o caminho com " / " entre os níveis, e esse caminho
// vai como rótulo do Prometheus. Nome de grupo que contenha o separador
// produz o mesmo rótulo que um caminho de dois níveis — e duas amostras
// com rótulos idênticos fazem o Prometheus recusar a raspagem inteira, com
// err_duplicate_sample_for_timestamp. Nenhuma métrica entra, nem a dos
// monitores saudáveis.
//
// É o desfecho que o rótulo de grupo existe para evitar.
func TestGroupNameCannotContainThePathSeparator(t *testing.T) {
	for _, nome := range []string{"prod / api", "a / b / c", "x /  y"} {
		g := domain.Group{Name: nome, Slug: "g"}
		if err := g.Validate(); err == nil {
			t.Errorf("Validate aceitou o nome %q, que colide com um caminho de dois níveis", nome)
		}
	}
}

// E só o separador inteiro. Recusar qualquer barra pegaria nomes que nunca
// colidiram com caminho nenhum — "TI/Infra" não é "TI / Infra" — e
// quebraria o salvamento desses grupos numa instalação que já os tem.
func TestOrdinaryGroupNamesStillValidate(t *testing.T) {
	for _, nome := range []string{
		"prod", "Retaguarda", "api-v2", "Faturamento (SP)", "pré-produção",
		"TI/Infra", "E/S", "24/7", "A/B",
	} {
		g := domain.Group{Name: nome, Slug: "g"}
		if err := g.Validate(); err != nil {
			t.Errorf("Validate recusou o nome %q: %v", nome, err)
		}
	}
}

// O byte de controle é barrado aqui, e não escapado na saída: o formato de
// exposição define três escapes, e qualquer outro que se invente colide
// com o texto literal que o coletor desfaz para a mesma coisa. ESC
// reprograma o terminal de quem lê /metrics cru; CR sobrescreve a linha.
func TestNamesRefuseControlCharacters(t *testing.T) {
	for _, nome := range []string{"svc\x1b[2J", "svc\r", "svc\n", "svc\x00", "svc\x7f"} {
		if err := monitorVálido(nome).Validate(); err == nil {
			t.Errorf("Monitor.Validate aceitou %q", nome)
		}
		g := domain.Group{Name: nome, Slug: "g"}
		if err := g.Validate(); err == nil {
			t.Errorf("Group.Validate aceitou %q", nome)
		}
	}
}

// Nome de monitor não tinha teto nenhum, ao contrário do de grupo, então
// ia até o limite do corpo da requisição — e ele sai no rótulo do
// Prometheus, numa exposição que é aberta por padrão.
//
// Medido em runes: a mensagem fala em caracteres, e um nome em português
// recusado na metade do que ela promete é defeito.
func TestMonitorNameCapCountsRunes(t *testing.T) {
	if err := monitorVálido(strings.Repeat("a", domain.MaxMonitorNameLength+1)).Validate(); err == nil {
		t.Errorf("Validate aceitou um nome de %d caracteres", domain.MaxMonitorNameLength+1)
	}
	if err := monitorVálido(strings.Repeat("a", domain.MaxMonitorNameLength)).Validate(); err != nil {
		t.Errorf("Validate recusou um nome no limite: %v", err)
	}
	// O que o teto de bytes recusava: cada "á" são dois bytes, então 120
	// deles davam 240 e caíam fora de um limite anunciado como 120.
	acentuado := strings.Repeat("á", domain.MaxMonitorNameLength)
	if err := monitorVálido(acentuado).Validate(); err != nil {
		t.Errorf("Validate recusou %d caracteres acentuados: %v", domain.MaxMonitorNameLength, err)
	}
}
