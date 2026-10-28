package probe

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Padrões de quem não diz nada: um minuto entre verificações é o ritmo
// do central, e dez segundos de timeout cabem com folga em qualquer
// intervalo aceito acima de vinte.
const (
	DefaultInterval = 60 * time.Second
	DefaultTimeout  = 10 * time.Second
)

// Duration é uma duração escrita como texto ("60s", "1m") no YAML.
//
// O time.Duration cru sairia em nanossegundos no arquivo, que quem abre
// o arquivo no terminal do jump server não tem como ler.
type Duration time.Duration

// String devolve a duração na notação do Go.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalYAML implementa yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// UnmarshalYAML implementa yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return fmt.Errorf("linha %d: duração %q inválida; use algo como 30s, 1m ou 5m", node.Line, text)
	}
	*d = Duration(parsed)
	return nil
}

// Config é o arquivo da sonda.
type Config struct {
	// Name identifica a sonda no sinal de vida ("sonda vm-prod-1 viva").
	Name string `yaml:"name,omitempty"`

	Heartbeat Heartbeat `yaml:"heartbeat,omitempty"`

	// AllowHTTPPush aceita push em HTTP sem TLS. Desligado por padrão: o
	// token viaja no caminho, e em claro qualquer um no trajeto o lê.
	AllowHTTPPush bool `yaml:"allow_http_push,omitempty"`

	// UIToken protege a interface web opcional. Gerado no setup.
	UIToken string `yaml:"ui_token,omitempty"`

	Checks []Check `yaml:"checks"`
}

// Heartbeat é o sinal de vida da própria sonda.
//
// Vai para um monitor push só dele no central. Quando a sonda para, é
// esse monitor que cai, e o aviso diz "a sonda sumiu" em vez de culpar
// cada serviço que ela vigiava.
type Heartbeat struct {
	PushURL  string   `yaml:"push_url,omitempty"`
	Interval Duration `yaml:"interval,omitempty"`
}

// Check é uma verificação local reportada por push.
type Check struct {
	Name   string `yaml:"name"`
	Type   string `yaml:"type"`
	Target string `yaml:"target"`

	Interval Duration `yaml:"interval,omitempty"`
	Timeout  Duration `yaml:"timeout,omitempty"`
	// DegradedLatency marca a verificação como degradada quando a
	// resposta passa disto. Zero desliga.
	DegradedLatency Duration `yaml:"degraded_latency,omitempty"`

	// PushURL é o endereço do monitor push no central, com o token.
	PushURL string `yaml:"push_url"`

	// Config é a configuração do checker, a mesma que o central aceita
	// em JSON, aqui escrita em YAML.
	Config map[string]any `yaml:"config,omitempty"`
}

// checkName limita o nome ao que passa intacto por um terminal e por um
// argumento de linha de comando, sem aspas.
var checkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName informa se o nome serve para uma verificação ou para a sonda.
func ValidName(name string) bool { return checkName.MatchString(name) }

var (
	registryOnce sync.Once
	registry     *checker.Registry
)

// NewRegistry monta os checkers que a sonda executa.
//
// É a mesma implementação do central: a mesma verificação dá o mesmo
// resultado dentro e fora da rede. Push fica de fora, porque a sonda é
// quem bate no central, não quem recebe.
func NewRegistry() *checker.Registry {
	r, err := checker.NewRegistry(
		checker.NewHTTP(),
		checker.NewTCP(),
		checker.NewTLS(),
		checker.NewDNS(),
		checker.NewICMP(),
		checker.NewRedis(),
		checker.NewPostgres(),
		checker.NewMySQL(),
	)
	if err != nil {
		// Só acontece com dois checkers do mesmo tipo na lista acima.
		panic(err)
	}
	return r
}

func validationRegistry() *checker.Registry {
	registryOnce.Do(func() { registry = NewRegistry() })
	return registry
}

// Types lista os tipos aceitos, na ordem em que o assistente os oferece.
var Types = []string{"http", "tcp", "redis", "postgres", "mysql", "dns", "tls", "icmp"}

// Monitor converte a verificação no monitor que o checker entende.
//
// O id só precisa ser único entre as verificações da sonda: o checker
// HTTP o usa para guardar as asserções compiladas.
func (c Check) Monitor(id int64) (domain.Monitor, error) {
	typ, err := domain.ParseMonitorType(c.Type)
	if err != nil {
		return domain.Monitor{}, fmt.Errorf("tipo %q desconhecido; use um de %s", c.Type, strings.Join(Types, ", "))
	}
	var raw json.RawMessage
	if len(c.Config) > 0 {
		raw, err = json.Marshal(c.Config)
		if err != nil {
			return domain.Monitor{}, fmt.Errorf("configuração do checker: %w", err)
		}
	}
	return domain.Monitor{
		ID:                    id,
		Name:                  c.Name,
		Type:                  typ,
		Target:                c.Target,
		Interval:              time.Duration(c.Interval),
		Timeout:               time.Duration(c.Timeout),
		DegradedLatency:       time.Duration(c.DegradedLatency),
		ConfirmationThreshold: 1,
		Config:                raw,
		Enabled:               true,
	}, nil
}

// applyDefaults preenche intervalo e timeout omitidos.
func (c *Config) applyDefaults() {
	if c.Heartbeat.PushURL != "" && c.Heartbeat.Interval == 0 {
		c.Heartbeat.Interval = Duration(DefaultInterval)
	}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.Interval == 0 {
			ch.Interval = Duration(DefaultInterval)
		}
		if ch.Timeout == 0 {
			ch.Timeout = Duration(min(DefaultTimeout, time.Duration(ch.Interval)/2))
		}
	}
}

// Validate confere a configuração inteira, apontando a verificação que
// reprovou.
func (c Config) Validate() error {
	if c.Name != "" && !ValidName(c.Name) {
		return fmt.Errorf("nome da sonda %q inválido; use letras, números, ponto, hífen e sublinhado", c.Name)
	}
	if c.Heartbeat.PushURL != "" {
		if err := ValidatePushURL(c.Heartbeat.PushURL, c.AllowHTTPPush); err != nil {
			return fmt.Errorf("sinal de vida: %w", err)
		}
		if time.Duration(c.Heartbeat.Interval) < domain.MinInterval {
			return fmt.Errorf("sinal de vida: o intervalo deve ser no mínimo %s", domain.MinInterval)
		}
	}

	seen := make(map[string]bool, len(c.Checks))
	for i, ch := range c.Checks {
		if seen[ch.Name] {
			return fmt.Errorf("verificação %q aparece mais de uma vez", ch.Name)
		}
		seen[ch.Name] = true
		if err := ch.validate(int64(i+1), c.AllowHTTPPush); err != nil {
			label := ch.Name
			if label == "" {
				label = fmt.Sprintf("#%d", i+1)
			}
			return fmt.Errorf("verificação %s: %w", label, err)
		}
	}
	return nil
}

func (c Check) validate(id int64, allowHTTP bool) error {
	if !ValidName(c.Name) {
		return fmt.Errorf("nome %q inválido; use até 64 letras, números, ponto, hífen ou sublinhado, sem espaço", c.Name)
	}
	if c.Type == "push" {
		return errors.New("o tipo push não roda na sonda: é a sonda que manda o push ao central")
	}
	m, err := c.Monitor(id)
	if err != nil {
		return err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if err := validationRegistry().Validate(m); err != nil {
		return err
	}
	return ValidatePushURL(c.PushURL, allowHTTP)
}

// ValidatePushURL confere o endereço de push do central.
//
// Exige o formato .../push/TOKEN, com o tamanho mínimo de token do
// central, para um endereço colado pela metade ser recusado aqui e não
// virar 401 silencioso a cada minuto. Usuário, query e fragmento são
// recusados: o sinal vai no corpo, e nada além do token deve morar no
// endereço.
func ValidatePushURL(raw string, allowHTTP bool) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("falta o endereço de push (https://.../api/v1/push/TOKEN)")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("endereço de push inválido")
	}
	switch {
	case u.Scheme == "http" && !allowHTTP:
		return errors.New("push em HTTP sem TLS exporia o token; use https (ou allow_http_push, só em rede confiável)")
	case u.Scheme != "https" && u.Scheme != "http":
		return errors.New("o endereço de push precisa começar com https://")
	case u.Host == "":
		return errors.New("o endereço de push não tem host")
	case u.User != nil:
		return errors.New("o endereço de push não pode ter usuário nem senha")
	case u.RawQuery != "" || u.Fragment != "":
		return errors.New("o endereço de push não leva query nem fragmento; o sinal vai no corpo")
	}

	token, ok := pushToken(u.Path)
	if !ok {
		return errors.New("o endereço de push precisa terminar em /push/TOKEN")
	}
	if len(token) < checker.MinPushTokenLength {
		return fmt.Errorf("o token do push tem %d caracteres; o central exige pelo menos %d",
			len(token), checker.MinPushTokenLength)
	}
	return nil
}

// pushToken extrai o token do caminho .../push/TOKEN.
func pushToken(path string) (string, bool) {
	i := strings.LastIndex(path, "/")
	dir, token := path[:i+1], path[i+1:]
	if token == "" || !strings.HasSuffix(dir, "/push/") {
		return "", false
	}
	return token, true
}

// maskedTokenPrefix é quanto do token fica visível: o bastante para
// distinguir dois monitores, longe do bastante para usar.
const maskedTokenPrefix = 4

// MaskURL devolve o endereço de push com o token escondido.
//
// Toda saída da sonda passa por aqui: a tela do jump server costuma ser
// gravada, e o token inteiro numa gravação é um monitor que qualquer um
// mantém verde.
func MaskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(endereço inválido)"
	}
	u.User = nil
	u.RawQuery, u.Fragment = "", ""
	if token, ok := pushToken(u.Path); ok {
		shown := token
		if len(shown) > maskedTokenPrefix {
			shown = shown[:maskedTokenPrefix]
		}
		u.Path = strings.TrimSuffix(u.Path, token) + shown + "…"
		return u.Scheme + "://" + u.Host + u.Path
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// Find devolve a verificação pelo nome.
func (c *Config) Find(name string) (*Check, bool) {
	for i := range c.Checks {
		if c.Checks[i].Name == name {
			return &c.Checks[i], true
		}
	}
	return nil, false
}

// Add inclui a verificação. Com replace, substitui a de mesmo nome em vez
// de recusar.
func (c *Config) Add(ch Check, replace bool) error {
	if existing, ok := c.Find(ch.Name); ok {
		if !replace {
			return fmt.Errorf("já existe uma verificação %q; use --replace para substituí-la", ch.Name)
		}
		*existing = ch
		return nil
	}
	c.Checks = append(c.Checks, ch)
	return nil
}

// Remove apaga a verificação pelo nome.
func (c *Config) Remove(name string) error {
	for i := range c.Checks {
		if c.Checks[i].Name == name {
			c.Checks = append(c.Checks[:i], c.Checks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("nenhuma verificação chamada %q", name)
}

// Parse lê o conteúdo do arquivo, aplica os padrões e valida.
func Parse(data []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Campo desconhecido é erro: "intervall" ignorado em silêncio faria a
	// verificação rodar com o padrão sem ninguém saber.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("arquivo de configuração: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Load lê e valida o arquivo. Arquivo inexistente devolve um erro que
// satisfaz errors.Is(err, os.ErrNotExist).
func Load(path string) (Config, error) {
	cfg, _, err := load(path)
	return cfg, err
}

func load(path string) (Config, [32]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, [32]byte{}, err
	}
	if err := checkPrivate(path); err != nil {
		return Config{}, [32]byte{}, err
	}
	cfg, err := Parse(data)
	return cfg, sha256.Sum256(data), err
}

// fileHeader abre o arquivo gravado, para quem o encontra no volume saber
// o que é e o cuidado que pede.
const fileHeader = `# Configuração da upwatch-probe.
#
# Guarda o token de cada push e, se houver, senhas: mantenha com
# permissão 600 e fora de variável de ambiente. O daemon relê este
# arquivo quando ele muda, sem reiniciar.
`

// Save valida e grava a configuração, com permissão 600.
func Save(path string, cfg Config) error {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := encode(cfg)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

func encode(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// updateAttempts limita as tentativas quando outro processo grava no meio.
const updateAttempts = 5

// Update lê, altera e grava a configuração.
//
// A CLI roda por docker exec ao lado do daemon, que tem a interface web
// e também grava. Antes de substituir o arquivo, Update confere que
// ninguém o mudou desde a leitura; se mudou, refaz a alteração sobre a
// versão nova em vez de apagar a do outro.
//
// Arquivo inexistente começa vazio: é o primeiro add numa sonda recém
// instalada.
func Update(path string, change func(*Config) error) (Config, error) {
	for range updateAttempts {
		cfg, sum, err := load(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			cfg = Config{}
		case err != nil:
			return Config{}, err
		}

		if err := change(&cfg); err != nil {
			return Config{}, err
		}
		cfg.applyDefaults()
		if err := cfg.Validate(); err != nil {
			return Config{}, err
		}
		data, err := encode(cfg)
		if err != nil {
			return Config{}, err
		}

		current, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
		if err == nil && sha256.Sum256(current) != sum {
			continue
		}
		return cfg, writeAtomic(path, data)
	}
	return Config{}, errors.New("o arquivo de configuração mudou durante a gravação várias vezes seguidas; tente de novo")
}

// writeAtomic grava num arquivo temporário do mesmo diretório e o move
// para o lugar.
//
// O daemon relê o arquivo quando ele muda; uma gravação direta deixaria
// ele ler o arquivo pela metade. O temporário nasce com 600, então o
// segredo nunca existe em disco com outra permissão.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".probe-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Depois do Rename o temporário não existe mais e a remoção falha;
	// ela só importa quando algo deu errado antes.
	defer func() { _ = os.Remove(name) }()

	// Nos caminhos de erro, o erro que importa é o da escrita, não o do
	// Close do arquivo que vai ser descartado.
	if err := tmp.Chmod(0o600); err != nil && !isWindows() {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
