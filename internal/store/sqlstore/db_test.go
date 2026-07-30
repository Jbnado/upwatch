package sqlstore

import (
	"testing"

	"github.com/bernardojoao/upwatch/internal/store/migrations"
)

// A tradução de marcadores é o ponto onde um SQL só serve a dois bancos.
//
// Errar aqui não quebra o build nem falha alto: produz uma consulta que
// o PostgreSQL aceita com os parâmetros trocados de lugar, ou que grava
// o valor errado na coluna errada.

func TestRebindDeixaSQLiteIntacto(t *testing.T) {
	const q = `SELECT id FROM monitor WHERE name = ? AND enabled = ?`

	if got := rebind(migrations.SQLite, q); got != q {
		t.Fatalf("SQLite teve o SQL alterado:\n%s", got)
	}
}

func TestRebindNumeraEmOrdem(t *testing.T) {
	got := rebind(migrations.Postgres, `INSERT INTO monitor (name, target, enabled) VALUES (?, ?, ?)`)
	want := `INSERT INTO monitor (name, target, enabled) VALUES ($1, $2, $3)`

	if got != want {
		t.Fatalf("numeração divergiu:\n got: %s\nwant: %s", got, want)
	}
}

func TestRebindPassaDeNove(t *testing.T) {
	// Dois dígitos é onde uma implementação ingênua quebra: o INSERT de
	// rollup tem quinze parâmetros.
	q := `VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	want := `VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	if got := rebind(migrations.Postgres, q); got != want {
		t.Fatalf("divergiu:\n got: %s\nwant: %s", got, want)
	}
}

func TestRebindIgnoraInterrogacaoDentroDeTexto(t *testing.T) {
	// A consulta de relatos compara com um literal, e uma interrogação
	// dentro de aspas viraria "$2" no meio da string — a comparação
	// passaria a ser com um texto que não existe.
	q := `SELECT ? FROM announcement WHERE phase <> 'onde está?'`
	want := `SELECT $1 FROM announcement WHERE phase <> 'onde está?'`

	if got := rebind(migrations.Postgres, q); got != want {
		t.Fatalf("divergiu:\n got: %s\nwant: %s", got, want)
	}
}

func TestRebindContinuaDepoisDoTexto(t *testing.T) {
	q := `WHERE nome = 'a?b' AND id = ? AND outro = ?`
	want := `WHERE nome = 'a?b' AND id = $1 AND outro = $2`

	if got := rebind(migrations.Postgres, q); got != want {
		t.Fatalf("divergiu:\n got: %s\nwant: %s", got, want)
	}
}

func TestRebindPreservaOperadorDeAtribuicao(t *testing.T) {
	// ON CONFLICT ... DO UPDATE aparece em quase todo upsert do schema.
	q := `INSERT INTO push_state (monitor_id, last_push) VALUES (?, ?)
		ON CONFLICT (monitor_id) DO UPDATE SET last_push = excluded.last_push`
	want := `INSERT INTO push_state (monitor_id, last_push) VALUES ($1, $2)
		ON CONFLICT (monitor_id) DO UPDATE SET last_push = excluded.last_push`

	if got := rebind(migrations.Postgres, q); got != want {
		t.Fatalf("divergiu:\n got: %s\nwant: %s", got, want)
	}
}
