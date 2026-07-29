package sqlstore_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bernardojoao/upwatch/internal/store"
	"github.com/bernardojoao/upwatch/internal/store/sqlstore"
	"github.com/bernardojoao/upwatch/internal/store/storetest"
)

// newStore devolve uma store limpa em arquivo temporário.
//
// Arquivo em vez de ":memory:" de propósito: cada conexão do pool abriria
// seu próprio banco em memória, e a suíte passaria contra um alvo que não
// existe em produção.
func newStore(t *testing.T) store.Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "upwatch.db")
	s, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close returned unexpected error: %v", err)
		}
	})
	return s
}

// TestSQLiteConformance é o contrato: se esta suíte falhar, o backend não
// serve. A mesma bateria roda contra PostgreSQL.
func TestSQLiteConformance(t *testing.T) {
	storetest.RunConformance(t, newStore)
}

func TestOpenSQLiteAppliesMigrations(t *testing.T) {
	s := newStore(t).(*sqlstore.Store)

	for _, table := range []string{"monitor", "heartbeat", "rollup", "rollup_state"} {
		t.Run(table, func(t *testing.T) {
			var name string
			err := s.DB().QueryRow(
				`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table,
			).Scan(&name)
			if err != nil {
				t.Fatalf("tabela %q não existe após as migrations: %v", table, err)
			}
		})
	}
}

// Migration que não desce impede rollback de uma versão com defeito.
func TestMigrationsRollBackCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	s, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer s.Close()

	if err := s.Rollback(); err != nil {
		t.Fatalf("Rollback returned unexpected error: %v", err)
	}

	var name string
	err = s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name = 'monitor'`).Scan(&name)
	if err == nil {
		t.Error("tabela monitor ainda existe após o rollback")
	}
}

// A cascata só funciona com foreign_keys ligado, e o pragma vale por
// conexão. Este teste confirma que a DSN o aplica em todas elas.
func TestForeignKeysPragmaIsEnabled(t *testing.T) {
	s := newStore(t).(*sqlstore.Store)

	var enabled int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatalf("PRAGMA foreign_keys returned unexpected error: %v", err)
	}
	if enabled != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", enabled)
	}
}

func TestWALJournalModeIsEnabled(t *testing.T) {
	s := newStore(t).(*sqlstore.Store)

	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode returned unexpected error: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("PRAGMA journal_mode = %q, want %q", mode, "wal")
	}
}

// A consulta por janela precisa usar o índice composto. Uma varredura
// completa aqui é a causa-raiz declarada do gargalo do Uptime Kuma, então
// o plano de execução é verificado e não apenas presumido.
func TestHeartbeatRangeQueryUsesIndex(t *testing.T) {
	s := newStore(t).(*sqlstore.Store)

	rows, err := s.DB().QueryContext(context.Background(), `
		EXPLAIN QUERY PLAN
		SELECT monitor_id, probe_id, ts, status, latency_ms, message
		FROM heartbeat
		WHERE monitor_id = ? AND ts >= ? AND ts < ?
		ORDER BY ts LIMIT ?`, 1, 0, 1, 10)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN returned unexpected error: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scanning query plan returned unexpected error: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteString("\n")
	}

	got := plan.String()
	if !strings.Contains(got, "idx_heartbeat_monitor_ts") {
		t.Errorf("plano de execução não usa idx_heartbeat_monitor_ts:\n%s", got)
	}
	if strings.Contains(got, "SCAN heartbeat") {
		t.Errorf("plano de execução faz varredura completa de heartbeat:\n%s", got)
	}
}
