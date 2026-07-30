package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
	"github.com/bernardojoao/upwatch/internal/store/storetest"
)

// A mesma bateria, contra PostgreSQL.
//
// É o que separa "banco plugável" de promessa: o segundo backend passa
// integralmente na suíte que o primeiro passa, ou não entra. Sem isto, o
// caminho de produção seria exercitado só em produção.
//
// Roda quando UPWATCH_TEST_POSTGRES_DSN aponta para um banco descartável;
// no CI o serviço sobe junto. Fora daí o teste se anuncia como pulado, e
// não em silêncio — teste que some sem avisar vira cobertura imaginária.

const postgresDSNEnv = "UPWATCH_TEST_POSTGRES_DSN"

func TestPostgresConformance(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("defina %s para rodar a conformidade contra PostgreSQL", postgresDSNEnv)
	}

	storetest.RunConformance(t, func(t *testing.T) store.Store {
		t.Helper()
		return newPostgresStore(t, dsn)
	})
}

// TestPostgresMigrationsRollBackCleanly confere que o schema desce.
//
// No SQLite o mesmo teste existe; aqui importa mais, porque é o banco em
// que voltar uma versão acontece com o serviço no ar.
func TestPostgresMigrationsRollBackCleanly(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("defina %s para rodar a conformidade contra PostgreSQL", postgresDSNEnv)
	}

	s := newPostgresStore(t, dsn).(*sqlstore.Store)
	if err := s.RollbackAll(); err != nil {
		t.Fatalf("RollbackAll devolveu erro: %v", err)
	}

	for _, tabela := range []string{"monitor", "status_page", "announcement"} {
		var existe bool
		err := s.DB().QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			 WHERE table_schema = current_schema() AND table_name = $1)`, tabela).Scan(&existe)
		if err != nil {
			t.Fatalf("consultando o catálogo: %v", err)
		}
		if existe {
			t.Errorf("tabela %q sobreviveu ao rollback", tabela)
		}
	}
}

// newPostgresStore devolve uma store limpa num schema próprio.
//
// Schema por teste, e não banco por teste: criar um banco custa segundos
// e a suíte tem mais de cem casos. O schema é derramado no fim, então um
// caso nunca enxerga o que outro escreveu — que é a mesma garantia que o
// arquivo temporário dá no SQLite.
func newPostgresStore(t *testing.T, dsn string) store.Store {
	t.Helper()

	schema := schemaName(t)

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("abrindo conexão administrativa: %v", err)
	}
	defer admin.Close()

	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("limpando schema anterior: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("criando schema: %v", err)
	}

	s, err := sqlstore.OpenPostgres(withSearchPath(dsn, schema))
	if err != nil {
		t.Fatalf("OpenPostgres devolveu erro: %v", err)
	}

	t.Cleanup(func() {
		_ = s.Close()

		limpeza, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer limpeza.Close()
		_, _ = limpeza.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})
	return s
}

// schemaName deriva um identificador válido do nome do teste.
func schemaName(t *testing.T) string {
	limpo := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, t.Name())

	// O PostgreSQL trunca identificadores em 63 bytes; truncar aqui evita
	// que dois testes de nome longo colidam num prefixo comum.
	if len(limpo) > 48 {
		limpo = limpo[:48]
	}
	return fmt.Sprintf("upw_%s", limpo)
}

// withSearchPath aponta a conexão para o schema do teste.
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}
