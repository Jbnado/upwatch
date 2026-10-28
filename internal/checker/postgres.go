package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// ValidateTarget confere o alvo no cadastro (ver splitTarget).
func (c *Postgres) ValidateTarget(target string) error { return validateTarget(target) }

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

// beginQuery abre a transação somente leitura e combina com o servidor o
// limite de tempo dela (ver serverLimit).
//
// O pgx já manda o pedido de cancelamento quando a conexão cai no meio da
// consulta, mas esse pedido chega por uma conexão nova, que um pooler ou
// firewall no caminho pode não deixar passar. O limite dentro da sessão
// não depende de nada disso: statement_timeout para a consulta, e
// idle_in_transaction_session_timeout encerra a sessão se o UpWatch sumir
// com a transação aberta.
//
// Vai em SET LOCAL, e não nos parâmetros de abertura da sessão: o
// PgBouncer recusa a sessão que chega com parâmetro que ele não conhece, e
// o SET LOCAL vale só dentro da transação, que é o que o modo transação
// dele preserva. Tudo segue na mesma ida ao servidor do BEGIN.
func beginQuery(limit time.Duration) string {
	ms := max(limit.Milliseconds(), 1)
	return fmt.Sprintf("BEGIN READ ONLY; "+
		"SET LOCAL default_transaction_read_only = on; "+
		"SET LOCAL statement_timeout = %d; "+
		"SET LOCAL lock_timeout = %d; "+
		"SET LOCAL idle_in_transaction_session_timeout = %d", ms, ms, ms)
}

// postgresClass traduz o erro do servidor pelo SQLSTATE, sem o texto (ver
// sqlFailure). As classes nomeadas são as que pedem uma ação diferente
// de quem lê o aviso; o resto fica com o código, que se procura na
// documentação.
func postgresClass(err error) (string, bool) {
	if pgconn.Timeout(err) {
		return "tempo esgotado", true
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", false
	}
	var class string
	switch {
	case strings.HasPrefix(pgErr.Code, "28"):
		class = "autenticação recusada"
	case pgErr.Code == "53300":
		class = "conexões esgotadas no servidor"
	case pgErr.Code == "57P03":
		class = "o servidor não está aceitando conexões"
	case pgErr.Code == "3D000":
		class = "o banco não existe"
	case pgErr.Code == "57014":
		class = "tempo esgotado no servidor"
	case pgErr.Code == "25006":
		class = "a consulta tentou escrever"
	case strings.HasPrefix(pgErr.Code, "42"):
		class = "a consulta foi recusada pelo servidor"
	default:
		class = "erro do servidor"
	}
	return fmt.Sprintf("%s (SQLSTATE %s)", class, pgErr.Code), true
}

// Check abre a sessão, executa a consulta numa transação somente leitura
// e fecha tudo.
func (c *Postgres) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parsePostgresConfig(m.Config)
	if err != nil {
		return down("%v", err)
	}
	host, port, err := splitTarget(m.Target)
	if err != nil {
		return down("%v", err)
	}
	meta := map[string]string{"host": host, "port": port}
	fail := func(stage string, err error) Result {
		return sqlFailure(m, meta, cfg.Password, stage, err, postgresClass)
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
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{BeginQuery: beginQuery(serverLimit(ctx))})
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
