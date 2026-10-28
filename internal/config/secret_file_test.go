package config_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/config"
)

// validKey é uma chave de segredos bem formada: 32 bytes em base64.
var validKey = base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

// secretFile grava o conteúdo num arquivo, como o Docker monta um secret.
func secretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile returned unexpected error: %v", err)
	}
	return path
}

// sensitiveOptions são as opções que aceitam _FILE, com como ler cada uma.
var sensitiveOptions = []struct {
	env   string
	value string
	read  func(config.Config) string
}{
	{"DB_DSN", "postgres://upwatch:s3nh4@db:5432/upwatch", func(c config.Config) string { return c.Database.DSN }},
	{"METRICS_TOKEN", "token-do-prometheus", func(c config.Config) string { return c.MetricsToken }},
	{"SECRET_KEY", validKey, func(c config.Config) string { return c.SecretKey }},
}

// Variável de ambiente aparece em docker inspect, em /proc/<pid>/environ e
// em dump de diagnóstico. O Swarm, o Kubernetes e o Compose montam segredo
// como arquivo justamente para não passar por aí, e a convenção das
// imagens oficiais é aceitar NOME_FILE com o caminho dele.
//
// O arquivo quase sempre termina em quebra de linha — é o que echo e
// editores gravam —, e ela não faz parte do valor: uma senha com "\n" no
// fim simplesmente não confere.
func TestSensitiveOptionsComeFromFile(t *testing.T) {
	for _, opt := range sensitiveOptions {
		t.Run(opt.env, func(t *testing.T) {
			for name, content := range map[string]string{
				"com quebra de linha":       opt.value + "\n",
				"com quebra do Windows":     opt.value + "\r\n",
				"sem quebra de linha":       opt.value,
				"com várias quebras no fim": opt.value + "\n\n",
			} {
				t.Run(name, func(t *testing.T) {
					t.Setenv("UPWATCH_"+opt.env+"_FILE", secretFile(t, content))
					cfg, err := config.Load("")
					if err != nil {
						t.Fatalf("Load returned unexpected error: %v", err)
					}
					if got := opt.read(cfg); got != opt.value {
						t.Errorf("value = %q, want %q", got, opt.value)
					}
				})
			}
		})
	}
}

// Espaço no meio ou no começo é do valor: uma senha pode tê-los, e só a
// quebra de linha final é ruído do arquivo.
func TestSecretFileKeepsWhatIsNotTheTrailingNewline(t *testing.T) {
	t.Setenv("UPWATCH_METRICS_TOKEN_FILE", secretFile(t, " token com espaço \n"))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.MetricsToken != " token com espaço " {
		t.Errorf("MetricsToken = %q, want the spaces kept", cfg.MetricsToken)
	}
}

// As duas formas ao mesmo tempo é engano de quem configurou — sobrou uma
// variável de antes da migração para secrets, por exemplo. Escolher uma
// em silêncio poderia subir com o valor velho; parar na partida diz o que
// corrigir.
func TestSensitiveOptionInBothFormsStopsStartup(t *testing.T) {
	for _, opt := range sensitiveOptions {
		t.Run(opt.env, func(t *testing.T) {
			t.Setenv("UPWATCH_"+opt.env, opt.value)
			t.Setenv("UPWATCH_"+opt.env+"_FILE", secretFile(t, opt.value))

			_, err := config.Load("")
			if err == nil {
				t.Fatal("Load accepted both forms at once")
			}
			for _, name := range []string{"UPWATCH_" + opt.env, "UPWATCH_" + opt.env + "_FILE"} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name %s", err, name)
				}
			}
		})
	}
}

// Arquivo que não existe ou veio vazio é secret mal montado. Subir com o
// valor padrão no lugar — banco SQLite em vez do PostgreSQL, /metrics
// aberto — esconderia o defeito até alguém notar a consequência.
func TestBrokenSecretFileStopsStartup(t *testing.T) {
	for name, path := range map[string]string{
		"inexistente": filepath.Join(t.TempDir(), "nao-existe"),
		"vazio":       secretFile(t, "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("UPWATCH_DB_DSN_FILE", path)
			_, err := config.Load("")
			if err == nil {
				t.Fatal("Load accepted a broken secret file")
			}
			if !strings.Contains(err.Error(), "UPWATCH_DB_DSN_FILE") {
				t.Errorf("error %q does not name the variable", err)
			}
		})
	}
}

// Uma chave malformada para na partida, antes de abrir o banco: descobrir
// só na hora de cifrar a primeira senha seria descobrir tarde.
func TestMalformedSecretKeyStopsStartup(t *testing.T) {
	t.Setenv("UPWATCH_SECRET_KEY", "minha senha")
	if _, err := config.Load(""); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Errorf("Load error = %v, want the key format explained", err)
	}
}

// A chave nova da rotação segue a mesma regra das outras opções sensíveis.
func TestLookupSecretReadsEitherForm(t *testing.T) {
	if _, ok, err := config.LookupSecret("NEW_SECRET_KEY"); ok || err != nil {
		t.Fatalf("LookupSecret without any variable = (%v, %v), want absent", ok, err)
	}

	t.Setenv("UPWATCH_NEW_SECRET_KEY_FILE", secretFile(t, validKey+"\n"))
	v, ok, err := config.LookupSecret("NEW_SECRET_KEY")
	if err != nil || !ok || v != validKey {
		t.Errorf("LookupSecret = (%q, %v, %v), want the key from the file", v, ok, err)
	}

	t.Setenv("UPWATCH_NEW_SECRET_KEY", validKey)
	if _, _, err := config.LookupSecret("NEW_SECRET_KEY"); err == nil {
		t.Error("LookupSecret accepted both forms at once")
	}
}
