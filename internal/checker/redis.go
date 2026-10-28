package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Jbnado/upwatch/internal/domain"
)

// RedisConfig é a configuração de um monitor de Redis. O alvo é host:porta.
type RedisConfig struct {
	// Username é o usuário de ACL (Redis 6+). Vazio usa o usuário default.
	Username string `json:"username,omitempty"`
	// Password é segredo: não volta na API (ver RedactConfig).
	Password string `json:"password,omitempty"`
	DB       int    `json:"db,omitempty"`

	TLS           bool `json:"tls,omitempty"`
	SkipTLSVerify bool `json:"skip_tls_verify,omitempty"`

	// MaxUsedMemoryBytes acusa queda quando used_memory passa do limite.
	// Um Redis sem memória responde PING e recusa escrita ("OOM command
	// not allowed"); o limite pega a situação antes disso. Zero desliga.
	MaxUsedMemoryBytes int64 `json:"max_used_memory_bytes,omitempty"`

	// Assertions são condições sobre comandos de leitura, avaliadas depois
	// do PING. Todas precisam valer.
	Assertions []RedisAssertion `json:"assertions,omitempty"`
}

// checks devolve as asserções a avaliar, com o limite de memória expresso
// como mais uma. Um mecanismo só para as duas coisas: a mensagem de causa
// e a forma de ler o INFO não divergem.
func (c RedisConfig) checks() []RedisAssertion {
	out := c.Assertions
	if c.MaxUsedMemoryBytes > 0 {
		out = append(append([]RedisAssertion(nil), out...), RedisAssertion{
			Command: "INFO", Section: "memory", Field: "used_memory",
			Op: "lte", Value: float64(c.MaxUsedMemoryBytes),
		})
	}
	return out
}

// Redis verifica um servidor Redis pelo protocolo, e não só pela porta.
type Redis struct{}

// NewRedis cria o checker de Redis.
func NewRedis() *Redis {
	silenceRedisLog.Do(func() { redis.SetLogger(discardRedisLog{}) })
	return &Redis{}
}

// silenceRedisLog cala o log interno do go-redis, que escreve no stderr a
// cada conexão recusada. Para um monitor, conexão recusada é o resultado
// da verificação, e já sai na causa; repeti-la no log a cada minuto
// afogaria o que o log do processo tem de útil.
var silenceRedisLog sync.Once

type discardRedisLog struct{}

func (discardRedisLog) Printf(context.Context, string, ...any) {}

// Type identifica o tipo de monitor atendido.
func (c *Redis) Type() domain.MonitorType { return domain.MonitorRedis }

// ValidateConfig confere a configuração no cadastro.
func (c *Redis) ValidateConfig(raw json.RawMessage) error {
	_, err := parseRedisConfig(raw)
	return err
}

func parseRedisConfig(raw json.RawMessage) (RedisConfig, error) {
	var cfg RedisConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração de Redis inválida: %w", err)
	}
	if hasWireBreak(cfg.Username) || hasWireBreak(cfg.Password) {
		return cfg, errors.New("checker: usuário e senha do Redis não podem ter quebra de linha nem NUL")
	}
	if cfg.DB < 0 {
		return cfg, errors.New("checker: o banco do Redis não pode ser negativo")
	}
	if cfg.MaxUsedMemoryBytes < 0 {
		return cfg, errors.New("checker: o limite de memória não pode ser negativo")
	}
	if len(cfg.Assertions) > MaxRedisAssertions {
		return cfg, fmt.Errorf("checker: no máximo %d asserções por monitor", MaxRedisAssertions)
	}
	for i, a := range cfg.Assertions {
		if err := a.Validate(); err != nil {
			return cfg, fmt.Errorf("asserção %d: %w", i+1, err)
		}
	}
	return cfg, nil
}

// hasWireBreak informa se o texto tem CR, LF ou NUL.
//
// Usuário, senha e chaves vão para qualquer host:porta cadastrado. O RESP
// os delimita pelo tamanho, mas um serviço que lê comandos por linha (o
// memcached, por exemplo) não: ali um CR ou LF no meio da senha vira um
// segundo comando, escrito por quem cadastrou o monitor e enviado pelo
// UpWatch. NUL termina texto em outros protocolos e não tem uso legítimo
// em nenhum desses campos.
func hasWireBreak(s string) bool {
	return strings.ContainsAny(s, "\r\n\x00")
}

// Check conecta, envia PING e avalia as asserções.
//
// Uma conexão nova por verificação, fechada em seguida: um pool parado
// entre checks esconderia justamente a falha de aceitar conexão nova
// (maxclients esgotado), e o custo de uma conexão por minuto é nada.
func (c *Redis) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseRedisConfig(m.Config)
	if err != nil {
		return down("%v", err)
	}
	host, port, err := net.SplitHostPort(m.Target)
	if err != nil || port == "" {
		return down("alvo precisa estar no formato host:porta, recebido %q", m.Target)
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	opts := &redis.Options{
		Addr:     m.Target,
		Username: cfg.Username,
		Password: cfg.Password,
		DB:       cfg.DB,
		// RESP2 conversa com qualquer versão; o RESP3 do padrão exige
		// HELLO, que um Redis anterior ao 6 não conhece.
		Protocol:              2,
		DialTimeout:           m.Timeout,
		ReadTimeout:           m.Timeout,
		WriteTimeout:          m.Timeout,
		ContextTimeoutEnabled: true,
		// Sem nova tentativa: ela mascararia a falha que se quer medir e
		// estouraria o timeout configurado.
		MaxRetries:    -1,
		DialerRetries: 1,
		PoolSize:      1,
		// Sem CLIENT SETINFO: o monitor não altera nada no servidor
		// monitorado, nem o nome de conexão.
		DisableIdentity: true,
	}
	if cfg.TLS {
		opts.TLSConfig = &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: cfg.SkipTLSVerify, //nolint:gosec // escolha explícita do operador
			MinVersion:         tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)
	defer client.Close()

	meta := map[string]string{"host": host, "port": port}

	start := time.Now()
	err = client.Ping(ctx).Err()
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return Result{
			Status:  domain.StatusDown,
			Message: scrubSecret("PING: "+err.Error(), cfg.Password),
			Meta:    meta,
		}
	}

	for _, a := range cfg.checks() {
		reply, err := client.Do(ctx, a.Args()...).Result()
		if errors.Is(err, redis.Nil) {
			reply, err = nil, nil
		}
		if err != nil {
			return Result{
				Status:    domain.StatusDown,
				LatencyMS: latency,
				Message:   scrubSecret(fmt.Sprintf("%s: %v", a.label(), err), cfg.Password),
				Meta:      meta,
			}
		}
		if cause := a.Evaluate(reply); cause != "" {
			return Result{
				Status:    domain.StatusDown,
				LatencyMS: latency,
				Message:   scrubSecret(cause, cfg.Password),
				Meta:      meta,
			}
		}
	}

	return Result{Status: domain.StatusUp, LatencyMS: latency, Meta: meta}
}
