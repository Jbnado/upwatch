package checker

import (
	"encoding/json"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
)

func httpMonitor(t *testing.T, id int64, as ...JSONAssertion) domain.Monitor {
	t.Helper()
	cfg, err := json.Marshal(HTTPConfig{JSONAssertions: as})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Monitor{ID: id, Type: domain.MonitorHTTP, Config: cfg}
}

// Interpretar as asserções é trabalho do cadastro; o Check roda a cada
// poucos segundos por monitor e não deveria refazê-lo. O mesmo monitor com
// a mesma configuração reaproveita o que já foi interpretado — a mesma
// fatia, não uma cópia igual.
func TestHTTPCompilesAssertionsOncePerConfig(t *testing.T) {
	h := NewHTTP()
	m := httpMonitor(t, 7, JSONAssertion{Path: "$.db", Op: opExists})

	first, err := h.compiledFor(m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.compiledFor(m)
	if err != nil {
		t.Fatal(err)
	}

	if &first.assertions[0] != &second.assertions[0] {
		t.Error("compiledFor compiled the same configuration twice")
	}
}

// Editar o monitor troca a configuração sem trocar o id. Servir a versão
// antiga do cache vigiaria o campo que a pessoa acabou de tirar.
func TestHTTPRecompilesAssertionsWhenConfigChanges(t *testing.T) {
	h := NewHTTP()
	if _, err := h.compiledFor(httpMonitor(t, 7, JSONAssertion{Path: "$.db", Op: opExists})); err != nil {
		t.Fatal(err)
	}

	got, err := h.compiledFor(httpMonitor(t, 7, JSONAssertion{Path: "$.cache", Op: opExists}))
	if err != nil {
		t.Fatal(err)
	}

	if len(got.assertions) != 1 || got.assertions[0].Path != "$.cache" {
		t.Errorf("compiledFor after an edit = %+v, want the new $.cache assertion", got.assertions)
	}
}

// httpMonitorRegex devolve um monitor HTTP com body_regex.
func httpMonitorRegex(t *testing.T, id int64, pattern string) domain.Monitor {
	t.Helper()
	cfg, err := json.Marshal(HTTPConfig{BodyRegex: pattern})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Monitor{ID: id, Type: domain.MonitorHTTP, Config: cfg}
}

// Compilar a expressão regular é o mesmo caso das asserções: trabalho de
// cadastro, pago a cada verificação. Um monitor de 10s com body_regex
// compilava o mesmo padrão 8.640 vezes por dia, e o custo cresce com o
// tamanho do padrão, não com o do corpo.
func TestHTTPCompilesBodyRegexOncePerConfig(t *testing.T) {
	h := NewHTTP()
	m := httpMonitorRegex(t, 11, `"uptime_seconds":\s*\d+`)

	first, err := h.compiledFor(m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.compiledFor(m)
	if err != nil {
		t.Fatal(err)
	}

	if first.bodyRegex == nil {
		t.Fatal("compiledFor did not compile the body regex")
	}
	if first.bodyRegex != second.bodyRegex {
		t.Error("compiledFor compiled the same body regex twice")
	}
}

// Trocar o padrão pela interface não troca o id do monitor. Servir o
// padrão antigo faria o monitor vigiar o que a pessoa acabou de tirar.
func TestHTTPRecompilesBodyRegexWhenConfigChanges(t *testing.T) {
	h := NewHTTP()
	if _, err := h.compiledFor(httpMonitorRegex(t, 11, `antigo`)); err != nil {
		t.Fatal(err)
	}

	got, err := h.compiledFor(httpMonitorRegex(t, 11, `novo`))
	if err != nil {
		t.Fatal(err)
	}

	if got.bodyRegex == nil || got.bodyRegex.String() != "novo" {
		t.Errorf("compiledFor after an edit = %v, want the new pattern", got.bodyRegex)
	}
}

// Monitor sem body_regex não guarda expressão nenhuma: o campo nulo é o
// que diz ao Check para não avaliar a condição.
func TestHTTPKeepsNoRegexWithoutBodyRegex(t *testing.T) {
	h := NewHTTP()

	got, err := h.compiledFor(httpMonitor(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	if got.bodyRegex != nil {
		t.Errorf("compiledFor without body_regex = %v, want nil", got.bodyRegex)
	}
}
