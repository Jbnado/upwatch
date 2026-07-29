package migrations_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/bernardojoao/upwatch/internal/store/migrations"
)

func TestFSExposesMigrationsForEachDialect(t *testing.T) {
	for _, d := range []migrations.Dialect{migrations.SQLite, migrations.Postgres} {
		t.Run(string(d), func(t *testing.T) {
			sub, err := migrations.FS(d)
			if err != nil {
				t.Fatalf("FS(%q) returned unexpected error: %v", d, err)
			}

			entries, err := fs.Glob(sub, "*.sql")
			if err != nil {
				t.Fatalf("Glob returned unexpected error: %v", err)
			}
			if len(entries) == 0 {
				t.Fatalf("no .sql migrations embedded for dialect %q", d)
			}
		})
	}
}

// Uma migration sem seção Down impede rollback, e o plano exige que o
// schema suba e desça.
func TestEveryMigrationDeclaresUpAndDown(t *testing.T) {
	for _, d := range []migrations.Dialect{migrations.SQLite, migrations.Postgres} {
		t.Run(string(d), func(t *testing.T) {
			sub, err := migrations.FS(d)
			if err != nil {
				t.Fatalf("FS(%q) returned unexpected error: %v", d, err)
			}
			names, _ := fs.Glob(sub, "*.sql")

			for _, name := range names {
				body, err := fs.ReadFile(sub, name)
				if err != nil {
					t.Fatalf("ReadFile(%s) returned unexpected error: %v", name, err)
				}
				sql := string(body)
				if !strings.Contains(sql, "-- +goose Up") {
					t.Errorf("%s/%s: falta a seção '-- +goose Up'", d, name)
				}
				if !strings.Contains(sql, "-- +goose Down") {
					t.Errorf("%s/%s: falta a seção '-- +goose Down'", d, name)
				}
			}
		})
	}
}

// Os dois dialetos precisam expor exatamente as mesmas versões, senão a
// suíte de conformidade estaria comparando schemas diferentes.
func TestDialectsHaveMatchingMigrationVersions(t *testing.T) {
	versions := map[migrations.Dialect][]string{}

	for _, d := range []migrations.Dialect{migrations.SQLite, migrations.Postgres} {
		sub, err := migrations.FS(d)
		if err != nil {
			t.Fatalf("FS(%q) returned unexpected error: %v", d, err)
		}
		names, _ := fs.Glob(sub, "*.sql")
		versions[d] = names
	}

	sqlite, postgres := versions[migrations.SQLite], versions[migrations.Postgres]
	if len(sqlite) != len(postgres) {
		t.Fatalf("sqlite tem %d migrations e postgres tem %d: %v vs %v",
			len(sqlite), len(postgres), sqlite, postgres)
	}
	for i := range sqlite {
		if sqlite[i] != postgres[i] {
			t.Errorf("migration %d difere entre dialetos: %q vs %q", i, sqlite[i], postgres[i])
		}
	}
}

func TestFSRejectsUnknownDialect(t *testing.T) {
	if _, err := migrations.FS("mongodb"); err == nil {
		t.Fatal("FS(\"mongodb\") returned nil error, want an error")
	}
}
