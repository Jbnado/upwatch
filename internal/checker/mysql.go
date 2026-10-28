package checker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// ValidateTarget confere o alvo no cadastro (ver splitTarget).
func (c *MySQL) ValidateTarget(target string) error { return validateTarget(target) }

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
	if writesFile(cfg.Query) {
		return cfg, errors.New("checker: a consulta não pode gravar arquivo (INTO OUTFILE ou INTO DUMPFILE)")
	}
	return cfg, nil
}

// writesFile informa se a consulta usa INTO OUTFILE ou INTO DUMPFILE.
//
// Os dois gravam um arquivo no servidor e passam pela transação somente
// leitura: contra o MySQL 8.4, um usuário com o privilégio FILE grava o
// arquivo mesmo dentro de START TRANSACTION READ ONLY. A palavra é
// procurada em qualquer posição, inclusive em comentário executável;
// palavra-chave do MySQL não tem grafia alternativa, e o falso positivo
// (a palavra dentro de um texto) é um preço pequeno.
func writesFile(query string) bool {
	q := strings.ToUpper(query)
	return strings.Contains(q, "OUTFILE") || strings.Contains(q, "DUMPFILE")
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

// mysqlClass traduz o erro do servidor pelo número e pelo SQLSTATE, sem o
// texto (ver sqlFailure).
func mysqlClass(err error) (string, bool) {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return "", false
	}
	var class string
	switch me.Number {
	case 1044, 1045, 1698:
		class = "autenticação recusada"
	case 1040, 1203:
		class = "conexões esgotadas no servidor"
	case 1049:
		class = "o banco não existe"
	case 3024:
		class = "tempo esgotado no servidor"
	case 1792:
		class = "a consulta tentou escrever"
	case 1064, 1142, 1146, 1054:
		class = "a consulta foi recusada pelo servidor"
	default:
		class = "erro do servidor"
	}
	// O erro do handshake não traz SQLSTATE; o número basta.
	if me.SQLState == [5]byte{} {
		return fmt.Sprintf("%s (erro %d)", class, me.Number), true
	}
	return fmt.Sprintf("%s (SQLSTATE %s, erro %d)", class, me.SQLState[:], me.Number), true
}

// Check abre a sessão, executa a consulta numa transação somente leitura
// e fecha tudo.
func (c *MySQL) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseMySQLConfig(m.Config)
	if err != nil {
		return down("%v", err)
	}
	host, port, err := splitTarget(m.Target)
	if err != nil {
		return down("%v", err)
	}
	meta := map[string]string{"host": host, "port": port}
	fail := func(stage string, err error) Result {
		return sqlFailure(m, meta, cfg.Password, stage, err, mysqlClass)
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

	queryCtx, stopReading := context.WithCancel(ctx)
	defer stopReading()
	rows, err := tx.QueryContext(queryCtx, cfg.query())
	if err != nil {
		return fail("consulta", err)
	}
	err = readRows(rows.Next, rows.Err, stopReading)
	elapsed := time.Since(start)
	rows.Close()
	if err != nil {
		return fail("consulta", err)
	}

	// SLEEP interrompido pelo limite devolve 1 em vez de erro; sem esta
	// conferência, uma consulta que estourou o tempo passaria por verde.
	if limit > 0 && elapsed >= limit {
		return fail("consulta", context.DeadlineExceeded)
	}

	return Result{
		Status:    domain.StatusUp,
		LatencyMS: elapsed.Milliseconds(),
		Meta:      meta,
	}
}
