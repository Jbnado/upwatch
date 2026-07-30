package sqlstore

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"github.com/Jbnado/upwatch/internal/store/migrations"
)

// A ponte entre um SQL só e dois dialetos.
//
// O schema foi desenhado para que a mesma consulta sirva aos dois bancos:
// tempo em inteiros, enums em texto, agregação em Go. Sobram duas
// diferenças que nenhum desenho de schema resolve, e as duas moram aqui.
//
// A primeira é o marcador de parâmetro. SQLite usa "?" e PostgreSQL usa
// "$1"; escrever cada consulta duas vezes dobraria a superfície onde as
// duas versões podem divergir em silêncio, então a tradução acontece na
// borda.
//
// A segunda é como se descobre o id recém-gerado. LastInsertId não existe
// no PostgreSQL, e RETURNING existe nos dois — então todo INSERT usa
// RETURNING, e não há caminho por dialeto.

// db envolve a conexão com o dialeto, traduzindo o SQL na borda.
type db struct {
	sql     *sql.DB
	dialect migrations.Dialect
}

// rebind traduz "?" para o marcador do dialeto.
//
// Ignora "?" dentro de literal entre aspas simples: uma mensagem gravada
// com interrogação viraria "$3" no meio do texto.
func rebind(dialect migrations.Dialect, query string) string {
	if dialect != migrations.Postgres {
		return query
	}

	var out strings.Builder
	out.Grow(len(query) + 8)

	n := 0
	dentroDeTexto := false

	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'':
			// Aspas duplicadas ('') são um apóstrofo escapado, não o fim
			// do literal; o próximo caractere segue dentro dele.
			dentroDeTexto = !dentroDeTexto
			out.WriteByte(c)
		case c == '?' && !dentroDeTexto:
			n++
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(n))
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func (d *db) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.sql.ExecContext(ctx, rebind(d.dialect, query), args...)
}

func (d *db) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.sql.QueryContext(ctx, rebind(d.dialect, query), args...)
}

func (d *db) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, rebind(d.dialect, query), args...)
}

func (d *db) BeginTx(ctx context.Context, opts *sql.TxOptions) (*tx, error) {
	t, err := d.sql.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &tx{sql: t, dialect: d.dialect}, nil
}

// insertID executa um INSERT e devolve o id gerado.
//
// Acrescenta RETURNING id à consulta. Vale nos dois bancos — SQLite o
// suporta desde a versão 3.35 — e evita LastInsertId, que o PostgreSQL
// não implementa.
func (d *db) insertID(ctx context.Context, query string, args ...any) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// tx é a transação com o mesmo tratamento de dialeto.
type tx struct {
	sql     *sql.Tx
	dialect migrations.Dialect
}

func (t *tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.sql.ExecContext(ctx, rebind(t.dialect, query), args...)
}

func (t *tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.sql.QueryRowContext(ctx, rebind(t.dialect, query), args...)
}

// PrepareContext prepara a consulta já traduzida.
//
// O escritor de lote prepara uma vez e executa milhares de vezes — é o
// caminho quente da gravação de batidas, e preparar por linha custaria
// mais que a própria escrita.
func (t *tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.sql.PrepareContext(ctx, rebind(t.dialect, query))
}

func (t *tx) insertID(ctx context.Context, query string, args ...any) (int64, error) {
	var id int64
	if err := t.QueryRowContext(ctx, query+" RETURNING id", args...).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (t *tx) Commit() error   { return t.sql.Commit() }
func (t *tx) Rollback() error { return t.sql.Rollback() }
