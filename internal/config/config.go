// Package config carrega as opções de infraestrutura.
//
// Precedência: variável de ambiente vence arquivo, que vence padrão. É a
// ordem que permite subir um compose com valores no YAML e sobrescrever
// um deles pontualmente sem editar arquivo.
//
// Aqui vivem apenas as decisões de instalação — porta, banco, retenção.
// O que é do domínio, como monitores e canais, mora no banco e é editado
// pela interface: exigir reinício para cadastrar um alvo seria hostil a
// quem opera.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/bernardojoao/upwatch/internal/rollup"
	"github.com/bernardojoao/upwatch/internal/scheduler"
	"github.com/bernardojoao/upwatch/internal/sentinel"
)

// envPrefix identifica as variáveis do UpWatch no ambiente.
const envPrefix = "UPWATCH_"

// Config são as opções de uma instalação.
type Config struct {
	// Listen é o endereço do servidor HTTP.
	Listen string `yaml:"listen"`

	// Database aponta o armazenamento.
	Database Database `yaml:"database"`

	// Retention é a cascata de retenção.
	Retention Retention `yaml:"retention"`

	// Workers limita checks simultâneos. Sem teto, mil monitores vencendo
	// juntos abririam mil conexões e esgotariam os descritores do processo.
	Workers int `yaml:"workers"`

	// RollupInterval é a frequência do ciclo de agregação e poda.
	RollupInterval time.Duration `yaml:"rollup_interval"`

	// SentinelTargets são os alvos que decidem se a rede do próprio
	// monitor está no ar. Lista vazia desliga a supressão.
	SentinelTargets []string `yaml:"sentinel_targets"`

	// SecureCookies marca o cookie de sessão como Secure. Desligado por
	// padrão porque muitas instalações servem HTTP puro na rede local, e
	// ali o cookie não seria enviado — o login pareceria quebrado.
	SecureCookies bool `yaml:"secure_cookies"`

	// SessionTTL é a validade de um login na interface.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// Database identifica o armazenamento.
type Database struct {
	// Driver é sqlite ou postgres.
	Driver string `yaml:"driver"`
	// DSN é o caminho do arquivo ou a string de conexão.
	DSN string `yaml:"dsn"`
}

// Retention é a cascata, em duração.
type Retention struct {
	Raw    time.Duration `yaml:"raw"`
	Hourly time.Duration `yaml:"hourly"`
	Daily  time.Duration `yaml:"daily"`
}

// Default é a configuração de uma instalação que não configurou nada.
//
// Precisa funcionar sem nenhuma variável definida: um `docker run` sem
// argumentos tem que subir e monitorar.
func Default() Config {
	return Config{
		Listen: ":8080",
		Database: Database{
			Driver: "sqlite",
			DSN:    "/data/upwatch.db",
		},
		Retention: Retention{
			Raw:    rollup.DefaultRetention.Raw,
			Hourly: rollup.DefaultRetention.Hourly,
			Daily:  rollup.DefaultRetention.Daily,
		},
		Workers:         scheduler.DefaultWorkers,
		RollupInterval:  rollup.DefaultInterval,
		SentinelTargets: sentinel.DefaultTargets,
		SecureCookies:   false,
		SessionTTL:      7 * 24 * time.Hour,
	}
}

// Load monta a configuração a partir do arquivo e do ambiente.
//
// O caminho do arquivo pode ser vazio; nesse caso só ambiente e padrões
// são considerados.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		if err := applyFile(&cfg, path); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyFile sobrepõe o que o arquivo declarar.
func applyFile(cfg *Config, path string) error {
	dados, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: lendo %s: %w", path, err)
	}
	// Decodificar sobre a struct já preenchida preserva os padrões nos
	// campos que o arquivo não menciona.
	if err := yaml.Unmarshal(dados, cfg); err != nil {
		return fmt.Errorf("config: interpretando %s: %w", path, err)
	}
	return nil
}

// applyEnv sobrepõe o que o ambiente declarar.
func applyEnv(cfg *Config) error {
	str(&cfg.Listen, "LISTEN")
	str(&cfg.Database.Driver, "DB_DRIVER")
	str(&cfg.Database.DSN, "DB_DSN")

	if err := dur(&cfg.Retention.Raw, "RETENTION_RAW"); err != nil {
		return err
	}
	if err := dur(&cfg.Retention.Hourly, "RETENTION_HOURLY"); err != nil {
		return err
	}
	if err := dur(&cfg.Retention.Daily, "RETENTION_DAILY"); err != nil {
		return err
	}
	if err := dur(&cfg.RollupInterval, "ROLLUP_INTERVAL"); err != nil {
		return err
	}
	if err := dur(&cfg.SessionTTL, "SESSION_TTL"); err != nil {
		return err
	}
	if err := num(&cfg.Workers, "WORKERS"); err != nil {
		return err
	}
	if err := boolean(&cfg.SecureCookies, "SECURE_COOKIES"); err != nil {
		return err
	}

	if raw, ok := lookup("SENTINEL_TARGETS"); ok {
		cfg.SentinelTargets = splitList(raw)
	}
	return nil
}

// Validate recusa combinações que só falhariam depois, em produção.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("config: listen não pode ser vazio")
	}
	if c.Database.Driver != "sqlite" && c.Database.Driver != "postgres" {
		return fmt.Errorf("config: driver de banco desconhecido %q; use sqlite ou postgres", c.Database.Driver)
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("config: dsn do banco não pode ser vazio")
	}
	if c.Workers <= 0 {
		return fmt.Errorf("config: workers precisa ser positivo")
	}
	if c.RollupInterval <= 0 {
		return fmt.Errorf("config: rollup_interval precisa ser positivo")
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("config: session_ttl precisa ser positivo")
	}

	// A validação da cascata vive no pacote que a aplica, para não haver
	// duas versões da mesma regra.
	return c.RetentionPolicy().Validate()
}

// RetentionPolicy traduz para o formato usado pelo worker.
func (c Config) RetentionPolicy() rollup.Retention {
	return rollup.Retention{
		Raw:    c.Retention.Raw,
		Hourly: c.Retention.Hourly,
		Daily:  c.Retention.Daily,
	}
}

// ---------- leitura do ambiente ----------

func lookup(name string) (string, bool) {
	v, ok := os.LookupEnv(envPrefix + name)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return strings.TrimSpace(v), true
}

func str(dst *string, name string) {
	if v, ok := lookup(name); ok {
		*dst = v
	}
}

func num(dst *int, name string) error {
	v, ok := lookup(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("config: %s%s precisa ser um número inteiro: %w", envPrefix, name, err)
	}
	*dst = parsed
	return nil
}

func boolean(dst *bool, name string) error {
	v, ok := lookup(name)
	if !ok {
		return nil
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("config: %s%s precisa ser verdadeiro ou falso: %w", envPrefix, name, err)
	}
	*dst = parsed
	return nil
}

// dur aceita a notação do Go e o sufixo de dias.
//
// "90d" é como um operador escreve retenção de três meses; obrigá-lo a
// converter para "2160h" seria transformar uma decisão de negócio numa
// conta de cabeça.
func dur(dst *time.Duration, name string) error {
	v, ok := lookup(name)
	if !ok {
		return nil
	}
	parsed, err := ParseDuration(v)
	if err != nil {
		return fmt.Errorf("config: %s%s: %w", envPrefix, name, err)
	}
	*dst = parsed
	return nil
}

// ParseDuration entende a notação do Go acrescida de dias.
func ParseDuration(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)

	if dias, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(dias, 64)
		if err != nil {
			return 0, fmt.Errorf("duração inválida %q", v)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("duração inválida %q; use por exemplo 30m, 12h ou 90d", v)
	}
	return d, nil
}

func splitList(raw string) []string {
	var out []string
	for _, parte := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(parte); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// UnmarshalYAML permite escrever "90d" também no arquivo.
func (r *Retention) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Raw    string `yaml:"raw"`
		Hourly string `yaml:"hourly"`
		Daily  string `yaml:"daily"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}

	for _, campo := range []struct {
		valor string
		dst   *time.Duration
		nome  string
	}{
		{raw.Raw, &r.Raw, "raw"},
		{raw.Hourly, &r.Hourly, "hourly"},
		{raw.Daily, &r.Daily, "daily"},
	} {
		if campo.valor == "" {
			continue
		}
		d, err := ParseDuration(campo.valor)
		if err != nil {
			return fmt.Errorf("retention.%s: %w", campo.nome, err)
		}
		*campo.dst = d
	}
	return nil
}
