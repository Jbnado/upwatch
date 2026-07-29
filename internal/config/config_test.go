package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/config"
)

// writeFile grava um arquivo de configuração temporário.
func writeFile(t *testing.T, conteudo string) string {
	t.Helper()

	caminho := filepath.Join(t.TempDir(), "upwatch.yaml")
	if err := os.WriteFile(caminho, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("WriteFile returned unexpected error: %v", err)
	}
	return caminho
}

// Um `docker run` sem argumento algum precisa subir e monitorar; exigir
// configuração para começar afastaria quem só quer experimentar.
func TestDefaultsAreUsableWithoutAnyConfiguration(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Listen == "" {
		t.Error("Listen is empty by default")
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("default driver = %q, want sqlite", cfg.Database.Driver)
	}
	if cfg.Workers <= 0 {
		t.Errorf("default workers = %d, want a positive value", cfg.Workers)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the default configuration does not validate: %v", err)
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	caminho := writeFile(t, `
listen: ":9000"
workers: 12
database:
  driver: postgres
  dsn: "postgres://localhost/upwatch"
`)

	cfg, err := config.Load(caminho)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Listen != ":9000" {
		t.Errorf("Listen = %q, want %q", cfg.Listen, ":9000")
	}
	if cfg.Workers != 12 {
		t.Errorf("Workers = %d, want 12", cfg.Workers)
	}
	if cfg.Database.Driver != "postgres" {
		t.Errorf("driver = %q, want postgres", cfg.Database.Driver)
	}
}

// Campo ausente no arquivo mantém o padrão; do contrário, mencionar uma
// opção zeraria todas as outras.
func TestFileKeepsDefaultsForUnmentionedFields(t *testing.T) {
	caminho := writeFile(t, `listen: ":9000"`)

	cfg, err := config.Load(caminho)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Workers != config.Default().Workers {
		t.Errorf("Workers = %d, want the default of %d", cfg.Workers, config.Default().Workers)
	}
	if cfg.Retention.Raw != config.Default().Retention.Raw {
		t.Errorf("Retention.Raw = %v, want the default", cfg.Retention.Raw)
	}
}

// É a ordem que permite subir um compose com valores no arquivo e
// sobrescrever um deles sem editá-lo.
func TestEnvironmentOverridesFile(t *testing.T) {
	caminho := writeFile(t, `listen: ":9000"`)
	t.Setenv("UPWATCH_LISTEN", ":7777")

	cfg, err := config.Load(caminho)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.Listen != ":7777" {
		t.Errorf("Listen = %q, want the environment to win with %q", cfg.Listen, ":7777")
	}
}

// "90d" é como se escreve retenção de três meses; obrigar a converter
// para "2160h" transformaria uma decisão de negócio em conta de cabeça.
func TestDurationsAcceptDaySuffix(t *testing.T) {
	t.Setenv("UPWATCH_RETENTION_HOURLY", "90d")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if want := 90 * 24 * time.Hour; cfg.Retention.Hourly != want {
		t.Errorf("Retention.Hourly = %v, want %v", cfg.Retention.Hourly, want)
	}
}

func TestDurationsAcceptGoNotation(t *testing.T) {
	t.Setenv("UPWATCH_ROLLUP_INTERVAL", "90s")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.RollupInterval != 90*time.Second {
		t.Errorf("RollupInterval = %v, want 90s", cfg.RollupInterval)
	}
}

func TestDaySuffixWorksInTheFileToo(t *testing.T) {
	caminho := writeFile(t, `
retention:
  raw: 3d
  hourly: 60d
  daily: 400d
`)

	cfg, err := config.Load(caminho)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if want := 3 * 24 * time.Hour; cfg.Retention.Raw != want {
		t.Errorf("Retention.Raw = %v, want %v", cfg.Retention.Raw, want)
	}
	if want := 400 * 24 * time.Hour; cfg.Retention.Daily != want {
		t.Errorf("Retention.Daily = %v, want %v", cfg.Retention.Daily, want)
	}
}

// Erro de digitação numa duração precisa reprovar no arranque, com a
// causa; descobrir só quando a poda rodar seria tarde.
func TestMalformedDurationFailsAtStartup(t *testing.T) {
	t.Setenv("UPWATCH_RETENTION_RAW", "sete dias")

	if _, err := config.Load(""); err == nil {
		t.Fatal("Load with a malformed duration returned nil error, want an error")
	}
}

func TestUnknownDatabaseDriverIsRejected(t *testing.T) {
	t.Setenv("UPWATCH_DB_DRIVER", "mongodb")

	if _, err := config.Load(""); err == nil {
		t.Fatal("Load with an unknown driver returned nil error, want an error")
	}
}

// Reter o agregado por menos tempo que o dado cru inverteria a cascata: o
// detalhe sobreviveria ao resumo que deveria substituí-lo.
func TestInvertedRetentionCascadeIsRejected(t *testing.T) {
	t.Setenv("UPWATCH_RETENTION_RAW", "30d")
	t.Setenv("UPWATCH_RETENTION_HOURLY", "1d")

	if _, err := config.Load(""); err == nil {
		t.Fatal("Load with an inverted cascade returned nil error, want an error")
	}
}

func TestSentinelTargetsComeAsList(t *testing.T) {
	t.Setenv("UPWATCH_SENTINEL_TARGETS", "1.1.1.1:53, 8.8.8.8:53")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if len(cfg.SentinelTargets) != 2 {
		t.Fatalf("SentinelTargets = %v, want two entries", cfg.SentinelTargets)
	}
	if cfg.SentinelTargets[1] != "8.8.8.8:53" {
		t.Errorf("second target = %q, want the surrounding spaces trimmed", cfg.SentinelTargets[1])
	}
}

// Lista vazia desliga a supressão por sentinela, para quem prefere
// receber todos os alertas.
func TestEmptySentinelListDisablesSuppression(t *testing.T) {
	t.Setenv("UPWATCH_SENTINEL_TARGETS", " , ")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if len(cfg.SentinelTargets) != 0 {
		t.Errorf("SentinelTargets = %v, want it empty", cfg.SentinelTargets)
	}
}

func TestMissingFileIsReported(t *testing.T) {
	if _, err := config.Load(filepath.Join(t.TempDir(), "nao-existe.yaml")); err == nil {
		t.Fatal("Load of a missing file returned nil error, want an error")
	}
}

func TestBooleanFromEnvironment(t *testing.T) {
	t.Setenv("UPWATCH_SECURE_COOKIES", "true")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if !cfg.SecureCookies {
		t.Error("SecureCookies = false, want the environment value to be applied")
	}
}
