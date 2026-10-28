package sqlstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// Atualizar uma instalação que já tem monitores push.
//
// A 00005 acrescenta colunas a tabelas com dados: o estado do sinal, a
// identidade dele, o instante que recomeça a espera e o token indexado.
// A suíte de conformidade só vê bancos criados do zero; quem atualiza
// chega com linhas que nasceram sem essas colunas, e é nelas que um
// padrão errado vira monitor caindo na manhã seguinte à atualização.

const sharedToken = "token-compartilhado-por-engano"

// prePushSignalVersion é a última versão antes da 00005. O teste desce
// até ela pelo número, e não um passo: com migrações depois da 00005, um
// passo só desfaria a última, e o banco "antigo" já teria as colunas do
// sinal.
const prePushSignalVersion = 4

// seedBeforePushSignal volta o schema para antes da 00005 e grava o que
// uma instalação daquela versão teria: dois monitores push que, por
// engano, compartilham o token, um HTTP, e o último sinal do primeiro.
//
// Os valores vão literais no SQL para servir aos dois dialetos sem
// depender da tradução de marcadores, que mora no store.
func seedBeforePushSignal(t *testing.T, s *sqlstore.Store) (first, second int64) {
	t.Helper()
	if err := s.RollbackTo(prePushSignalVersion); err != nil {
		t.Fatalf("RollbackTo returned unexpected error: %v", err)
	}

	ctx := context.Background()
	_, err := s.DB().ExecContext(ctx, `
		INSERT INTO monitor (name, type, target, interval_ms, timeout_ms, config, created_at, updated_at)
		VALUES
			('cron A', 'push', '', 60000, 10000, '{"token":"`+sharedToken+`"}', 1000, 9000),
			('cron B', 'push', '', 60000, 10000, '{"token":"`+sharedToken+`"}', 2000, 9000),
			('api', 'http', 'https://example.com', 60000, 10000, '{}', 3000, 9000)`)
	if err != nil {
		t.Fatalf("seeding monitors returned unexpected error: %v", err)
	}
	for name, id := range map[string]*int64{"cron A": &first, "cron B": &second} {
		if err := s.DB().QueryRowContext(ctx,
			`SELECT id FROM monitor WHERE name = '`+name+`'`).Scan(id); err != nil {
			t.Fatalf("reading id of %q returned unexpected error: %v", name, err)
		}
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO push_state (monitor_id, last_push) VALUES (`+strconv.FormatInt(first, 10)+`, 5000)`); err != nil {
		t.Fatalf("seeding push_state returned unexpected error: %v", err)
	}
	return first, second
}

// checkPushSignalUpgrade confere o banco depois de a 00005 rodar sobre os
// dados semeados.
func checkPushSignalUpgrade(t *testing.T, s *sqlstore.Store, first, second int64) {
	t.Helper()
	ctx := context.Background()

	// O sinal antigo significa o que sempre significou: "up", sem causa e
	// sem medição. E já foi contado — passou pelo motor antes da
	// atualização —, então a primeira verificação depois dela não o conta
	// de novo.
	sig, ok, err := s.LastPush(ctx, first)
	if err != nil || !ok {
		t.Fatalf("LastPush = (%+v, %v, %v), want the signal recorded before the upgrade", sig, ok, err)
	}
	if !sig.At.Equal(time.UnixMilli(5000).UTC()) {
		t.Errorf("At = %v, want the instant recorded before the upgrade", sig.At)
	}
	if sig.Status != domain.StatusUp || sig.Message != "" || sig.LatencyMS != 0 {
		t.Errorf("signal = %+v, want a bare up", sig)
	}
	if !sig.Evaluated {
		t.Errorf("signal = %+v, want the pre-upgrade signal treated as already counted", sig)
	}

	// O sinal que chega depois é inédito, e conta.
	if err := s.RecordPush(ctx, first, domain.PushSignal{At: time.UnixMilli(6000), Status: domain.StatusDown}); err != nil {
		t.Fatalf("RecordPush returned unexpected error: %v", err)
	}
	if sig, _, err = s.LastPush(ctx, first); err != nil || sig.Evaluated {
		t.Errorf("LastPush after a new signal = (%+v, %v), want it fresh", sig, err)
	}

	// A espera recomeça da criação, de onde a primeira janela já contava;
	// nenhum monitor ganha nem perde prazo com a atualização.
	a, err := s.Monitors().Get(ctx, first)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if !a.RearmedAt.Equal(a.CreatedAt) {
		t.Errorf("RearmedAt = %v, want the creation time %v", a.RearmedAt, a.CreatedAt)
	}

	// O token vai para a coluna. Dos dois que o compartilhavam, fica com o
	// de menor id — o que a busca antiga achava primeiro —, para o sinal
	// continuar chegando onde chegava.
	if a.PushToken != sharedToken {
		t.Errorf("PushToken = %q, want the token copied from the config", a.PushToken)
	}
	owner, err := s.Monitors().FindByPushToken(ctx, sharedToken)
	if err != nil || owner.ID != first {
		t.Errorf("FindByPushToken = (%d, %v), want monitor %d", owner.ID, err, first)
	}
	b, err := s.Monitors().Get(ctx, second)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if b.PushToken != "" {
		t.Errorf("duplicate kept PushToken = %q, want it left without the column", b.PushToken)
	}
	// E não pode reivindicá-lo de novo ao ser salvo.
	b.PushToken = sharedToken
	if err := s.Monitors().Update(ctx, b); !errors.Is(err, store.ErrConflict) {
		t.Errorf("Update of the duplicate returned %v, want store.ErrConflict", err)
	}
}

func TestPushSignalMigrationUpgradesExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	before, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	first, second := seedBeforePushSignal(t, before)
	if err := before.Close(); err != nil {
		t.Fatalf("Close returned unexpected error: %v", err)
	}

	after, err := sqlstore.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopening applied the migration with error: %v", err)
	}
	defer after.Close()

	checkPushSignalUpgrade(t, after, first, second)
}

func TestPostgresPushSignalMigrationUpgradesExistingRows(t *testing.T) {
	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("defina %s para rodar a conformidade contra PostgreSQL", postgresDSNEnv)
	}

	before := newPostgresStore(t, dsn).(*sqlstore.Store)
	first, second := seedBeforePushSignal(t, before)

	after, err := sqlstore.OpenPostgres(withSearchPath(dsn, schemaName(t)))
	if err != nil {
		t.Fatalf("reopening applied the migration with error: %v", err)
	}
	defer after.Close()

	checkPushSignalUpgrade(t, after, first, second)
}
