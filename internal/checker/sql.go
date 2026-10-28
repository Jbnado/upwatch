package checker

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// MaxSQLQueryLength limita o tamanho da consulta de verificação. Consulta
// de monitor é uma linha; algo maior que isto é relatório, e relatório
// rodando a cada minuto vira carga no banco que se queria vigiar.
const MaxSQLQueryLength = 4096

// defaultSQLQuery é a consulta quando nenhuma é configurada: prova que o
// banco aceita sessão e executa, sem tocar em tabela nenhuma.
const defaultSQLQuery = "SELECT 1"

// sqlConfig reúne o que PostgreSQL e MySQL têm em comum.
//
// O alvo fica no Target (host:porta), não numa cadeia de conexão: o
// endereço aparece no painel e na página pública, e uma DSN carrega a
// senha junto.
type sqlConfig struct {
	Username string `json:"username"`
	// Password é segredo: não volta na API (ver RedactConfig).
	Password string `json:"password,omitempty"`
	Database string `json:"database,omitempty"`
	// Query é executada numa transação somente leitura. Vazia usa SELECT 1.
	Query string `json:"query,omitempty"`
}

func (c sqlConfig) validate(kind string) error {
	if strings.TrimSpace(c.Username) == "" {
		return fmt.Errorf("checker: o usuário do %s não pode ser vazio", kind)
	}
	if c.Query != "" && strings.TrimSpace(c.Query) == "" {
		return errors.New("checker: a consulta não pode ser só espaço; deixe vazia para usar SELECT 1")
	}
	if len(c.Query) > MaxSQLQueryLength {
		return fmt.Errorf("checker: a consulta passa de %d caracteres", MaxSQLQueryLength)
	}
	return nil
}

func (c sqlConfig) query() string {
	if c.Query == "" {
		return defaultSQLQuery
	}
	return c.Query
}

// splitTarget confere o formato host:porta do alvo.
func splitTarget(target string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port == "" {
		return "", "", false
	}
	return host, port, true
}
