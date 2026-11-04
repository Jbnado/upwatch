package config_test

import (
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/config"
)

// contem confere se algum aviso fala do assunto.
func contem(avisos []string, trecho string) bool {
	for _, a := range avisos {
		if strings.Contains(a, trecho) {
			return true
		}
	}
	return false
}

// A exposição aberta de /metrics publica nome, grupo, estado e latência de
// todos os monitores — a topologia interna. Em loopback isso não sai da
// máquina; num endereço alcançável da rede, sai, e quem opera merece ler
// isso na partida e não descobrir pelo que um estranho raspou.
func TestMetricsWarningDependsOnTheListenAddress(t *testing.T) {
	casos := map[string]bool{
		":8080":           true,
		"0.0.0.0:8080":    true,
		"[::]:8080":       true,
		"192.168.1.5:443": true,
		"semporta":        true,
		"127.0.0.1:8080":  false,
		"127.1.2.3:8080":  false,
		"localhost:8080":  false,
		"[::1]:8080":      false,
	}
	for listen, esperado := range casos {
		t.Run(listen, func(t *testing.T) {
			cfg := config.Config{Listen: listen}
			avisou := contem(cfg.DeploymentWarnings(), "/metrics")
			if avisou != esperado {
				t.Fatalf("listen %q: avisou=%v, esperado %v (avisos: %q)",
					listen, avisou, esperado, cfg.DeploymentWarnings())
			}
		})
	}
}

// Com token definido, /metrics já exige credencial: o aviso seria ruído, e
// aviso que aparece quando não precisa é o que ensina a ignorar os outros.
func TestNoMetricsWarningWhenTheTokenIsSet(t *testing.T) {
	cfg := config.Config{Listen: "0.0.0.0:8080", MetricsToken: "segredo"}

	if avisos := cfg.DeploymentWarnings(); contem(avisos, "/metrics") {
		t.Fatalf("avisou sobre /metrics com token definido: %q", avisos)
	}
}

// É a combinação que entrega o cookie de sessão em claro numa instalação
// que se acha protegida: o endereço público é https, então existe TLS em
// algum ponto, mas o cookie não está marcado como Secure e vai junto numa
// requisição em http puro — por um link antigo, um redirecionamento
// perdido ou um atacante na mesma rede.
func TestSecureCookieWarningOnHTTPSPublicURL(t *testing.T) {
	casos := []struct {
		nome      string
		publicURL string
		secure    bool
		esperado  bool
	}{
		{"https sem secure", "https://status.exemplo.com", false, true},
		{"https maiúsculo", "HTTPS://status.exemplo.com", false, true},
		{"https com secure", "https://status.exemplo.com", true, false},
		{"http sem secure", "http://status.exemplo.com", false, false},
		{"sem endereço público", "", false, false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			cfg := config.Config{
				Listen:        "127.0.0.1:8080",
				PublicURL:     c.publicURL,
				SecureCookies: c.secure,
			}
			avisou := contem(cfg.DeploymentWarnings(), "UPWATCH_SECURE_COOKIES")
			if avisou != c.esperado {
				t.Fatalf("avisou=%v, esperado %v (avisos: %q)",
					avisou, c.esperado, cfg.DeploymentWarnings())
			}
		})
	}
}

// Uma instalação pode ter os dois problemas, e o segundo aviso não pode
// depender de o primeiro não existir.
func TestWarningsAccumulate(t *testing.T) {
	cfg := config.Config{
		Listen:    "0.0.0.0:8080",
		PublicURL: "https://status.exemplo.com",
	}

	avisos := cfg.DeploymentWarnings()
	if len(avisos) != 2 {
		t.Fatalf("avisos = %q, esperado dois", avisos)
	}
	if !contem(avisos, "/metrics") || !contem(avisos, "UPWATCH_SECURE_COOKIES") {
		t.Fatalf("avisos = %q", avisos)
	}
}

// A configuração padrão escuta em todas as interfaces, então ela avisa —
// e é o caso que mais importa, porque é o que a imagem sobe sem ninguém
// configurar nada. O aviso diz como calar: token ou loopback. O cookie
// não entra aqui, porque sem endereço público declarado nada indica que
// exista https na frente.
func TestDefaultConfigurationWarnsAboutOpenMetrics(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if avisos := cfg.DeploymentWarnings(); len(avisos) != 1 || !contem(avisos, "/metrics") {
		t.Fatalf("avisos da configuração padrão = %q", avisos)
	}
}
