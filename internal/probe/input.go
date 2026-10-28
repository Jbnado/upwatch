package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
)

// CheckInput são as opções de uma verificação como chegam da CLI ou do
// formulário da interface: texto de uma linha, sem JSON aninhado.
//
// Um caminho só monta a verificação a partir delas, para o add e a
// interface não divergirem no que aceitam.
type CheckInput struct {
	Name, Type, Target, PushURL string
	Interval, Timeout           time.Duration

	// Asserts são asserções de Redis em forma de linha.
	Asserts []string
	// ExpectStatus e BodyContains valem para HTTP.
	ExpectStatus []int
	BodyContains string
	// Username e Password são o usuário do Redis ou do banco, ou o basic
	// auth do HTTP.
	Username, Password string
	// Database e Query valem para PostgreSQL e MySQL.
	Database, Query string
	// ConfigJSON é a configuração completa do checker, para o que não tem
	// opção própria. As opções acima são aplicadas por cima dela.
	ConfigJSON string
}

// credentialFields diz onde cada tipo guarda usuário e senha.
var credentialFields = map[string][2]string{
	"redis":    {"username", "password"},
	"postgres": {"username", "password"},
	"mysql":    {"username", "password"},
	"http":     {"basic_auth_user", "basic_auth_pass"},
}

// Check monta a verificação. A validação completa (checker e push)
// acontece ao gravar, junto do resto da configuração.
func (in CheckInput) Check() (Check, error) {
	cfg := map[string]any{}
	if strings.TrimSpace(in.ConfigJSON) != "" {
		if err := json.Unmarshal([]byte(in.ConfigJSON), &cfg); err != nil {
			return Check{}, errors.New("--config-json precisa ser um objeto JSON válido")
		}
	}

	only := func(set bool, opt string, types ...string) error {
		if !set {
			return nil
		}
		for _, t := range types {
			if in.Type == t {
				return nil
			}
		}
		return fmt.Errorf("%s não vale para o tipo %s (só para %s)", opt, in.Type, strings.Join(types, ", "))
	}
	if err := errors.Join(
		only(len(in.Asserts) > 0, "--assert", "redis"),
		only(len(in.ExpectStatus) > 0, "--expect-status", "http"),
		only(in.BodyContains != "", "--body-contains", "http"),
		only(in.Database != "", "--database", "postgres", "mysql"),
		only(in.Query != "", "--query", "postgres", "mysql"),
		only(in.Username != "" || in.Password != "", "usuário e senha", "redis", "postgres", "mysql", "http"),
	); err != nil {
		return Check{}, err
	}

	if len(in.Asserts) > 0 {
		list := make([]any, 0, len(in.Asserts))
		for _, line := range in.Asserts {
			a, err := ParseRedisAssertion(line)
			if err != nil {
				return Check{}, err
			}
			list = append(list, toMap(a))
		}
		cfg["assertions"] = list
	}
	if len(in.ExpectStatus) > 0 {
		codes := make([]any, len(in.ExpectStatus))
		for i, c := range in.ExpectStatus {
			codes[i] = c
		}
		cfg["expect_status"] = codes
	}
	set := func(key, value string) {
		if value != "" {
			cfg[key] = value
		}
	}
	set("body_contains", in.BodyContains)
	set("database", in.Database)
	set("query", in.Query)
	if f, ok := credentialFields[in.Type]; ok {
		set(f[0], in.Username)
		set(f[1], in.Password)
	}

	c := Check{
		Name: in.Name, Type: in.Type, Target: strings.TrimSpace(in.Target), PushURL: strings.TrimSpace(in.PushURL),
		Interval: Duration(in.Interval), Timeout: Duration(in.Timeout),
	}
	if len(cfg) > 0 {
		c.Config = cfg
	}
	cf := Config{Checks: []Check{c}}
	cf.applyDefaults()
	return cf.Checks[0], nil
}

// toMap passa a estrutura do checker para o mapa do YAML, pelo JSON que
// o checker lê: os nomes dos campos são os dele, não uma segunda lista.
func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// secretKeys são as chaves que nunca aparecem numa listagem.
var secretKeys = map[string]bool{"password": true, "basic_auth_pass": true, "headers": true}

// Describe resume a configuração da verificação numa linha, sem
// segredo: as asserções de Redis na mesma forma que o add aceita, e as
// demais opções como chave=valor.
func Describe(c Check) string {
	var parts []string
	keys := make([]string, 0, len(c.Config))
	for k := range c.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := c.Config[k]
		switch {
		case k == "assertions" && c.Type == "redis":
			var list []checker.RedisAssertion
			b, _ := json.Marshal(v)
			if json.Unmarshal(b, &list) == nil {
				for _, a := range list {
					parts = append(parts, FormatRedisAssertion(a))
				}
				continue
			}
			parts = append(parts, k+"=?")
		case secretKeys[k]:
			parts = append(parts, k+"=***")
		default:
			b, _ := json.Marshal(v)
			parts = append(parts, k+"="+string(b))
		}
	}
	return strings.Join(parts, "; ")
}
