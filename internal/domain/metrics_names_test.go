package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// GroupTree.Path monta o caminho com " / " entre os níveis, e esse caminho
// vai como rótulo do Prometheus. Nome de grupo que contenha o separador
// produz o mesmo rótulo que um caminho de dois níveis — e duas amostras
// com rótulos idênticos fazem o Prometheus recusar a raspagem inteira, com
// err_duplicate_sample_for_timestamp. Nenhuma métrica entra, nem a dos
// monitores saudáveis.
//
// É o desfecho que o rótulo de grupo existe para evitar.
func TestGroupNameCannotContainThePathSeparator(t *testing.T) {
	for _, nome := range []string{"prod / api", "prod/api", "a /b", "a/ b"} {
		g := domain.Group{Name: nome, Slug: "g"}
		if err := g.Validate(); err == nil {
			t.Errorf("Validate aceitou o nome %q, que colide com um caminho de dois níveis", nome)
		}
	}
}

// Nome normal continua passando: a guarda não pode custar o uso comum.
func TestOrdinaryGroupNamesStillValidate(t *testing.T) {
	for _, nome := range []string{"prod", "Retaguarda", "api-v2", "Faturamento (SP)", "pré-produção"} {
		g := domain.Group{Name: nome, Slug: "g"}
		if err := g.Validate(); err != nil {
			t.Errorf("Validate recusou o nome %q: %v", nome, err)
		}
	}
}

// Nome de monitor não tinha teto nenhum, ao contrário do de grupo, então
// ia até o limite do corpo da requisição — e ele sai no rótulo do
// Prometheus, numa exposição que é aberta por padrão.
func TestMonitorNameHasACap(t *testing.T) {
	m := domain.Monitor{
		Name: strings.Repeat("a", domain.MaxMonitorNameLength+1), Type: domain.MonitorHTTP,
		Target: "https://exemplo.com", Interval: time.Minute, Timeout: 5 * time.Second,
		ConfirmationThreshold: 1,
	}
	if err := m.Validate(); err == nil {
		t.Errorf("Validate aceitou um nome de %d caracteres", domain.MaxMonitorNameLength+1)
	}

	m.Name = strings.Repeat("a", domain.MaxMonitorNameLength)
	if err := m.Validate(); err != nil {
		t.Errorf("Validate recusou um nome no limite: %v", err)
	}
}
