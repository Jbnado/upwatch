package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Jbnado/upwatch/internal/domain"
)

// postgresSSLModes são os valores aceitos de sslmode, os mesmos do libpq.
var postgresSSLModes = map[string]bool{
	"disable": true, "allow": true, "prefer": true,
	"require": true, "verify-ca": true, "verify-full": true,
}

// PostgresConfig é a configuração de um monitor de PostgreSQL. O alvo é
// host:porta.
type PostgresConfig struct {
	sqlConfig
	// SSLMode segue o libpq. Vazio é "prefer": tenta TLS e aceita sem.
	SSLMode string `json:"sslmode,omitempty"`
}

// Postgres verifica um PostgreSQL abrindo sessão e executando a consulta.
//
// A porta aberta não diz nada sobre um PostgreSQL sem conexões livres
// ("too many clients already"): ele aceita TCP e recusa a sessão. Só
// autenticar e consultar pega essa falha.
type Postgres struct{}

// NewPostgres cria o checker de PostgreSQL.
func NewPostgres() *Postgres { return &Postgres{} }

// Type identifica o tipo de monitor atendido.
func (c *Postgres) Type() domain.MonitorType { return domain.MonitorPostgres }

// ValidateConfig confere a configuração no cadastro.
func (c *Postgres) ValidateConfig(raw json.RawMessage) error {
	_, err := parsePostgresConfig(raw)
	return err
}

func parsePostgresConfig(raw json.RawMessage) (PostgresConfig, error) {
	var cfg PostgresConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração de PostgreSQL inválida: %w", err)
	}
	if err := cfg.validate("PostgreSQL"); err != nil {
		return cfg, err
	}
	if cfg.SSLMode != "" && !postgresSSLModes[cfg.SSLMode] {
		return cfg, fmt.Errorf("checker: sslmode %q desconhecido; use disable, allow, prefer, require, verify-ca ou verify-full", cfg.SSLMode)
	}
	return cfg, nil
}

// connConfig monta a configuração do pgx.
func (cfg PostgresConfig) connConfig(target string, timeout time.Duration) (*pgx.ConnConfig, error) {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "prefer"
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.User(cfg.Username),
		Host:   target,
		Path:   "/" + cfg.Database,
		RawQuery: url.Values{
			"sslmode":         {sslmode},
			"connect_timeout": {strconv.Itoa(max(1, int(timeout.Seconds())))},
		}.Encode(),
	}
	cc, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, err
	}
	// A senha entra depois do parse, e sempre: senão uma senha vazia
	// cairia no PGPASSWORD ou no .pgpass do host do monitor, e a
	// verificação passaria com uma credencial que ninguém configurou.
	cc.Password = cfg.Password
	// Protocolo estendido sem cache de comando preparado: recusa mais de
	// um comando na mesma consulta e funciona atrás de PgBouncer em modo
	// transação.
	cc.DefaultQueryExecMode = pgx.QueryExecModeExec
	return cc, nil
}

// Check abre a sessão, executa a consulta numa transação somente leitura
// e fecha tudo.
func (c *Postgres) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parsePostgresConfig(m.Config)
	if err != nil {
		return down("%v", err)
	}
	host, port, ok := splitTarget(m.Target)
	if !ok {
		return down("alvo precisa estar no formato host:porta, recebido %q", m.Target)
	}
	meta := map[string]string{"host": host, "port": port}
	fail := func(stage string, err error) Result {
		return Result{
			Status:  domain.StatusDown,
			Message: scrubSecret(stage+": "+err.Error(), cfg.Password),
			Meta:    meta,
		}
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	cc, err := cfg.connConfig(m.Target, m.Timeout)
	if err != nil {
		return fail("configuração", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return fail("conexão", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()

	start := time.Now()
	// Somente leitura mesmo que o usuário tenha permissão de escrita: uma
	// consulta de verificação que altera dado, repetida a cada minuto, é
	// um estrago que ninguém percebe até ser tarde.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fail("transação", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	rows, err := tx.Query(ctx, cfg.query())
	if err != nil {
		return fail("consulta", err)
	}
	for rows.Next() {
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail("consulta", err)
	}

	return Result{
		Status:    domain.StatusUp,
		LatencyMS: time.Since(start).Milliseconds(),
		Meta:      meta,
	}
}
