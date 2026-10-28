package sqlstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// Uma instalação anterior aos grupos migra sem perder nada.
//
// O banco é levado de volta à versão 4 — a última antes dos grupos —,
// recebe dados escritos à mão com o schema daquela época e é reaberto,
// como acontece quando alguém atualiza o binário. Escrever com SQL cru, e
// não pelo store, é o que garante que o dado tem a forma antiga: o store
// de hoje já escreveria na forma nova.
const preGroupsVersion = 4

// seedLegacy grava uma instalação típica da versão 4: uma conta, um
// monitor com histórico, um canal vinculado e uma queda resolvida.
func seedLegacy(t *testing.T, s *sqlstore.Store) {
	t.Helper()

	for _, q := range []string{
		`INSERT INTO app_user (id, username, password_hash, role, created_at, updated_at)
		 VALUES (1, 'ana', 'hash', 'viewer', 1, 1)`,
		`INSERT INTO monitor (id, name, type, target, interval_ms, timeout_ms, created_at, updated_at)
		 VALUES (1, 'api', 'http', 'https://exemplo.com', 60000, 10000, 1, 1)`,
		`INSERT INTO heartbeat (monitor_id, ts, status, latency_ms, message)
		 VALUES (1, 1000, 'down', 0, 'connection refused')`,
		`INSERT INTO notification_channel (id, name, type, config, enabled, created_at, updated_at)
		 VALUES (1, 'discord', 'webhook', '{}', 1, 1, 1)`,
		`INSERT INTO monitor_channel (monitor_id, channel_id) VALUES (1, 1)`,
		`INSERT INTO incident (monitor_id, started_at, resolved_at, cause)
		 VALUES (1, 1000, 2000, 'connection refused')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("seeding the legacy schema: %v\n%s", err, q)
		}
	}
}

// checkLegacyKept confere, pelo store de hoje, que nada se perdeu.
func checkLegacyKept(t *testing.T, s store.Store) {
	t.Helper()
	ctx := context.Background()

	m, err := s.Monitors().Get(ctx, 1)
	if err != nil {
		t.Fatalf("the monitor did not survive the migration: %v", err)
	}
	if m.GroupID != nil {
		t.Errorf("the legacy monitor landed in group %d, want \"Sem grupo\"", *m.GroupID)
	}

	u, err := s.Users().Get(ctx, 1)
	if err != nil {
		t.Fatalf("the account did not survive the migration: %v", err)
	}
	// Restringir na migração trancaria a instalação para fora.
	if u.Scope.Restricted {
		t.Errorf("the legacy account became restricted: %+v", u.Scope)
	}

	canais, err := s.Channels().Destinations(ctx, 1)
	if err != nil || len(canais) != 1 {
		t.Errorf("the monitor-channel link was lost: %v %+v", err, canais)
	}

	incidentes, err := s.Incidents().List(ctx, store.IncidentFilter{})
	if err != nil || len(incidentes.Items) != 1 {
		t.Errorf("the incident history was lost: %v %+v", err, incidentes.Items)
	}

	beats, err := s.QueryHeartbeats(ctx, store.HeartbeatQuery{
		MonitorID: 1, Range: store.TimeRange{From: time.UnixMilli(0), To: time.UnixMilli(10_000)},
	})
	if err != nil || len(beats) != 1 || beats[0].Message != "connection refused" {
		t.Errorf("the heartbeat history was lost: %v %+v", err, beats)
	}
}

func TestSQLiteMigrationKeepsLegacyInstallation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legado.db")

	s, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	if err := s.RollbackTo(preGroupsVersion); err != nil {
		t.Fatalf("RollbackTo returned unexpected error: %v", err)
	}
	seedLegacy(t, s)
	_ = s.Close()

	migrado, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopening the legacy database returned unexpected error: %v", err)
	}
	defer migrado.Close()

	checkLegacyKept(t, migrado)
}

func TestPostgresMigrationKeepsLegacyInstallation(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("defina %s para rodar a conformidade contra PostgreSQL", postgresDSNEnv)
	}

	s := newPostgresStore(t, dsn).(*sqlstore.Store)
	if err := s.RollbackTo(preGroupsVersion); err != nil {
		t.Fatalf("RollbackTo returned unexpected error: %v", err)
	}
	seedLegacy(t, s)

	migrado, err := sqlstore.OpenPostgres(withSearchPath(dsn, schemaName(t)))
	if err != nil {
		t.Fatalf("reopening the legacy database returned unexpected error: %v", err)
	}
	defer migrado.Close()

	checkLegacyKept(t, migrado)
}
