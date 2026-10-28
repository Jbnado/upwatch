package checker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Jbnado/upwatch/internal/domain"
)

// mysqlTLSModes são os valores aceitos em tls, os mesmos do driver.
var mysqlTLSModes = map[string]bool{
	"false": true, "true": true, "skip-verify": true, "preferred": true,
}

// MySQLConfig é a configuração de um monitor de MySQL ou MariaDB. O alvo
// é host:porta.
type MySQLConfig struct {
	sqlConfig
	// TLS é false (padrão), true, skip-verify ou preferred.
	TLS string `json:"tls,omitempty"`
}

// MySQL verifica um MySQL ou MariaDB abrindo sessão e executando a
// consulta. Pega o "Too many connections", que a porta aberta não mostra.
type MySQL struct{}

// NewMySQL cria o checker de MySQL.
func NewMySQL() *MySQL { return &MySQL{} }

// Type identifica o tipo de monitor atendido.
func (c *MySQL) Type() domain.MonitorType { return domain.MonitorMySQL }

// ValidateConfig confere a configuração no cadastro.
func (c *MySQL) ValidateConfig(raw json.RawMessage) error {
	_, err := parseMySQLConfig(raw)
	return err
}

func parseMySQLConfig(raw json.RawMessage) (MySQLConfig, error) {
	var cfg MySQLConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração de MySQL inválida: %w", err)
	}
	if err := cfg.validate("MySQL"); err != nil {
		return cfg, err
	}
	if cfg.TLS != "" && !mysqlTLSModes[cfg.TLS] {
		return cfg, fmt.Errorf("checker: tls %q desconhecido; use false, true, skip-verify ou preferred", cfg.TLS)
	}
	return cfg, nil
}

// mysqlUnknownVariable é o erro de variável de sistema desconhecida.
const mysqlUnknownVariable = 1193

// limitStatementTime combina com o servidor o limite da consulta (ver
// serverLimit). O go-sql-driver só fecha o socket no timeout, e o MySQL
// segue executando a consulta de uma conexão que já não existe.
//
// O MySQL chama o limite de max_execution_time, em milissegundos, e o
// MariaDB de max_statement_time, em segundos; cada um desconhece o do
// outro. Um servidor antigo que não tenha nenhum dos dois fica sem limite,
// em vez de sem monitor.
func limitStatementTime(ctx context.Context, conn *sql.Conn, limit time.Duration) error {
	if limit <= 0 {
		return nil
	}
	_, err := conn.ExecContext(ctx, fmt.Sprintf("SET SESSION max_execution_time = %d", max(limit.Milliseconds(), 1)))
	if !isMySQLError(err, mysqlUnknownVariable) {
		return err
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("SET SESSION max_statement_time = %.3f", limit.Seconds()))
	if isMySQLError(err, mysqlUnknownVariable) {
		return nil
	}
	return err
}

func isMySQLError(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}

// Check abre a sessão, executa a consulta numa transação somente leitura
// e fecha tudo.
func (c *MySQL) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseMySQLConfig(m.Config)
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

	dc := mysql.NewConfig()
	dc.User = cfg.Username
	dc.Passwd = cfg.Password
	dc.Net = "tcp"
	dc.Addr = m.Target
	dc.DBName = cfg.Database
	dc.TLSConfig = cfg.TLS
	dc.Timeout = m.Timeout
	dc.ReadTimeout = m.Timeout
	dc.WriteTimeout = m.Timeout
	// Sem vários comandos por consulta (o padrão do driver, dito aqui para
	// ninguém ligar): é o que impede a consulta de verificação de carregar
	// um segundo comando fora da transação somente leitura.
	dc.MultiStatements = false

	connector, err := mysql.NewConnector(dc)
	if err != nil {
		return fail("configuração", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		return fail("conexão", err)
	}
	defer conn.Close()

	limit := serverLimit(ctx)
	if err := limitStatementTime(ctx, conn, limit); err != nil {
		return fail("sessão", err)
	}

	start := time.Now()
	// Somente leitura mesmo que o usuário possa escrever, pela mesma razão
	// do PostgreSQL: consulta de verificação não pode alterar dado.
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fail("transação", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, cfg.query())
	if err != nil {
		return fail("consulta", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fail("consulta", err)
	}
	rows.Close()

	// SLEEP interrompido pelo limite devolve 1 em vez de erro; sem esta
	// conferência, uma consulta que estourou o tempo passaria por verde.
	if elapsed := time.Since(start); limit > 0 && elapsed >= limit {
		return fail("consulta", context.DeadlineExceeded)
	}

	return Result{
		Status:    domain.StatusUp,
		LatencyMS: time.Since(start).Milliseconds(),
		Meta:      meta,
	}
}
