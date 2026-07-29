// Package migrations embarca o schema de cada dialeto no binário.
//
// Embarcar evita que a imagem Docker precise carregar arquivos .sql
// soltos: um binário estático já sabe migrar o próprio banco.
package migrations

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed sqlite/*.sql postgres/*.sql
var files embed.FS

// Dialect identifica qual conjunto de migrations usar.
type Dialect string

const (
	// SQLite é o backend padrão: zero configuração.
	SQLite Dialect = "sqlite"
	// Postgres é o caminho de produção.
	Postgres Dialect = "postgres"
)

// known lista os dialetos com migrations embarcadas. A checagem explícita
// é necessária porque fs.Sub aceita qualquer caminho e devolve um sistema
// de arquivos vazio — um dialeto errado passaria silenciosamente e o banco
// subiria sem tabela alguma.
var known = map[Dialect]bool{
	SQLite:   true,
	Postgres: true,
}

// FS devolve o sistema de arquivos com as migrations do dialeto, já
// enraizado no diretório correspondente.
func FS(d Dialect) (fs.FS, error) {
	if !known[d] {
		return nil, fmt.Errorf("migrations: dialeto %q desconhecido", d)
	}
	sub, err := fs.Sub(files, string(d))
	if err != nil {
		return nil, fmt.Errorf("migrations: dialeto %q: %w", d, err)
	}
	return sub, nil
}
