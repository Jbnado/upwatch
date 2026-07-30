package sqlstore_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
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

	tabelas := []string{
		"monitor", "heartbeat", "rollup", "rollup_state",
		"status_page", "status_page_group", "status_page_component",
		"announcement", "announcement_component", "announcement_update",
	}
	for _, table := range tabelas {
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
//
// Desce todas, e não só a última: uma migration antiga sem reversão
// passaria despercebida até o dia em que alguém precisasse voltar uma
// versão — justamente o pior dia para descobrir.
func TestMigrationsRollBackCleanly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.db")
	s, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer s.Close()

	if err := s.RollbackAll(); err != nil {
		t.Fatalf("RollbackAll returned unexpected error: %v", err)
	}

	// Uma tabela de cada migration: a primeira e a das páginas públicas.
	for _, tabela := range []string{"monitor", "status_page", "announcement"} {
		var name string
		err = s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, tabela).Scan(&name)
		if err == nil {
			t.Errorf("tabela %q ainda existe após o rollback", tabela)
		}
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

// Apagar linhas no SQLite não encolhe o arquivo por si só: as páginas
// ficam livres para reuso e o arquivo mantém o pico histórico. Sem
// recuperar esse espaço, o banco pararia de crescer sem nunca devolver
// nada — exatamente a queixa que levou o Uptime Kuma a expor um botão
// manual de VACUUM.
func TestCompactReclaimsSpaceAfterPruning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compact.db")
	s, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	m := domain.Monitor{
		Name: "api", Type: domain.MonitorHTTP, Target: "https://example.com",
		Interval: time.Minute, Timeout: 10 * time.Second,
		ConfirmationThreshold: 1, Enabled: true,
	}
	if err := s.Monitors().Create(ctx, &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}

	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	const rows = 60_000
	for chunk := 0; chunk < rows; chunk += 5_000 {
		batch := make([]domain.Heartbeat, 0, 5_000)
		for i := 0; i < 5_000; i++ {
			batch = append(batch, domain.Heartbeat{
				MonitorID: m.ID,
				Timestamp: base.Add(time.Duration(chunk+i) * time.Second),
				Status:    domain.StatusUp,
				LatencyMS: 100,
				Message:   "",
			})
		}
		if err := s.WriteHeartbeats(ctx, batch); err != nil {
			t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
		}
	}

	peak := dbSize(t, path)

	if _, err := s.PruneHeartbeats(ctx, base.Add(rows*time.Second)); err != nil {
		t.Fatalf("PruneHeartbeats returned unexpected error: %v", err)
	}
	if err := s.Compact(ctx); err != nil {
		t.Fatalf("Compact returned unexpected error: %v", err)
	}

	after := dbSize(t, path)
	if after >= peak {
		t.Errorf("database is %d bytes after compacting, want less than the %d byte peak", after, peak)
	}
}

// dbSize soma o arquivo principal e o WAL, que é onde as páginas ficam
// até o checkpoint.
func dbSize(t *testing.T, path string) int64 {
	t.Helper()

	var total int64
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("Stat(%s) returned unexpected error: %v", path+suffix, err)
		}
		total += info.Size()
	}
	return total
}

func TestCompactOnEmptyStoreIsHarmless(t *testing.T) {
	s := newStore(t)

	if err := s.Compact(context.Background()); err != nil {
		t.Errorf("Compact on an empty store returned unexpected error: %v", err)
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
