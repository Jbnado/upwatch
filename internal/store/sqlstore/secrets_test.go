package sqlstore_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

// testKey é a chave da suíte. Fixa no pacote, para um teste que fecha e
// reabre o banco reencontrar o que gravou.
var testKey = func() secretbox.Key {
	k, err := secretbox.GenerateKey()
	if err != nil {
		panic(err)
	}
	return k
}()

// testKeys entrega a caixa da chave da suíte.
var testKeys = keysOf(testKey)

func keysOf(primary secretbox.Key, fallbacks ...secretbox.Key) secretbox.Source {
	box, err := secretbox.New(primary, fallbacks...)
	if err != nil {
		panic(err)
	}
	return secretbox.Fixed(box)
}

func newKey(t *testing.T) secretbox.Key {
	t.Helper()
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey returned unexpected error: %v", err)
	}
	return k
}

// backend abre, e reabre, um mesmo banco com a chave dada.
type backend struct {
	open func(t *testing.T, keys secretbox.Source) (*sqlstore.Store, error)
}

func (b backend) mustOpen(t *testing.T, keys secretbox.Source) *sqlstore.Store {
	t.Helper()
	s, err := b.open(t, keys)
	if err != nil {
		t.Fatalf("opening the database returned unexpected error: %v", err)
	}
	return s
}

// eachBackend roda o teste contra SQLite e, com a variável definida,
// contra PostgreSQL: a cifragem vive na camada comum, e os dois precisam
// prová-la.
func eachBackend(t *testing.T, run func(t *testing.T, b backend)) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "upwatch.db")
		run(t, backend{open: func(t *testing.T, keys secretbox.Source) (*sqlstore.Store, error) {
			s, err := sqlstore.OpenSQLite(path, keys)
			if s != nil {
				t.Cleanup(func() { _ = s.Close() })
			}
			return s, err
		}})
	})
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv(postgresDSNEnv)
		if dsn == "" {
			t.Skipf("defina %s para rodar contra PostgreSQL", postgresDSNEnv)
		}
		// Um schema limpo por teste, reaberto a cada abertura e derrubado
		// no fim, depois de os stores fecharem.
		schema := schemaName(t)
		if err := freshSchema(dsn, schema); err != nil {
			t.Fatalf("creating the schema returned unexpected error: %v", err)
		}
		t.Cleanup(func() { _ = dropSchema(dsn, schema) })
		run(t, backend{open: func(t *testing.T, keys secretbox.Source) (*sqlstore.Store, error) {
			s, err := sqlstore.OpenPostgres(withSearchPath(dsn, schema), keys)
			if s != nil {
				t.Cleanup(func() { _ = s.Close() })
			}
			return s, err
		}})
	})
}

// freshSchema recria o schema do teste vazio, para a primeira abertura
// encontrar um banco que nunca viu chave nenhuma.
func freshSchema(dsn, schema string) error {
	if err := dropSchema(dsn, schema); err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`CREATE SCHEMA ` + schema)
	return err
}

func dropSchema(dsn, schema string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	return err
}

func rawConfig(t *testing.T, s *sqlstore.Store, table string, id int64) string {
	t.Helper()
	var v string
	if err := s.DB().QueryRow(`SELECT config FROM ` + table + ` WHERE id = ` + strconv.FormatInt(id, 10)).Scan(&v); err != nil {
		t.Fatalf("reading %s %d returned unexpected error: %v", table, id, err)
	}
	return v
}

func createRedis(t *testing.T, s *sqlstore.Store, name, password string) domain.Monitor {
	t.Helper()
	m := domain.Monitor{
		Name: name, Type: domain.MonitorRedis, Target: "redis:6379",
		Interval: 60e9, Timeout: 10e9, ConfirmationThreshold: 3, Enabled: true,
		Config: json.RawMessage(`{"password":"` + password + `"}`),
	}
	if err := s.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return m
}

func createNtfy(t *testing.T, s *sqlstore.Store, token string) domain.Channel {
	t.Helper()
	c := domain.Channel{Name: "plantão", Type: "ntfy", Enabled: true,
		Config: json.RawMessage(`{"topic":"plantao","token":"` + token + `"}`)}
	if err := s.Channels().Create(context.Background(), &c); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return c
}

// Com a chave errada, o UpWatch subiria sem conseguir abrir senha
// nenhuma: todo monitor com credencial falharia na verificação, e o
// aviso chegaria como queda dos serviços monitorados. Recusar a partida
// diz o que está errado, e diz antes de qualquer verificação.
func TestWrongKeyRefusesToStart(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		s := b.mustOpen(t, testKeys)
		createRedis(t, s, "cache", "senha-do-cache")
		_ = s.Close()

		_, err := b.open(t, keysOf(newKey(t)))
		if !errors.Is(err, secretbox.ErrWrongKey) {
			t.Fatalf("opening with another key error = %v, want ErrWrongKey", err)
		}
	})
}

// Mesmo um banco sem nenhuma credencial guardada já tem chave: o
// registro-sentinela nasce na primeira subida. Sem ele, a segunda subida
// com outra chave passaria, e a primeira senha cadastrada depois ficaria
// cifrada com uma chave diferente da das que vierem a seguir.
func TestWrongKeyIsCaughtBeforeAnySecretExists(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		_ = b.mustOpen(t, testKeys).Close()

		if _, err := b.open(t, keysOf(newKey(t))); !errors.Is(err, secretbox.ErrWrongKey) {
			t.Fatalf("opening an empty database with another key error = %v, want ErrWrongKey", err)
		}
	})
}

// Sem chave à vista num banco que já tem segredo cifrado, nada de gerar
// uma nova: ela não abriria nada, e o arquivo dela, ao lado do banco,
// ainda faria a próxima subida parecer configurada.
func TestMissingKeyRefusesToStart(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		_ = b.mustOpen(t, testKeys).Close()

		auto := filepath.Join(t.TempDir(), "secret.key")
		_, err := b.open(t, secretbox.Resolver{AutoPath: auto}.Box)
		if !errors.Is(err, secretbox.ErrKeyMissing) {
			t.Fatalf("opening without a key error = %v, want ErrKeyMissing", err)
		}
		if _, statErr := os.Stat(auto); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("a key was generated for a database that already had one: %v", statErr)
		}
	})
}

// O dado associado prende o valor cifrado ao registro em que nasceu.
// Quem escreve no banco sem ter a chave não consegue copiar a senha do
// Redis de produção para um monitor apontado a um servidor seu: o
// UpWatch recusa abri-la ali, em vez de entregá-la na próxima
// verificação.
func TestCipherCopiedToAnotherRecordDoesNotOpen(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		s := b.mustOpen(t, testKeys)
		prod := createRedis(t, s, "producao", "senha-de-producao")
		mine := createRedis(t, s, "servidor-do-atacante", "qualquer")

		if _, err := s.DB().Exec(`UPDATE monitor SET config = '` + rawConfig(t, s, "monitor", prod.ID) +
			`' WHERE id = ` + strconv.FormatInt(mine.ID, 10)); err != nil {
			t.Fatalf("copying the cipher returned unexpected error: %v", err)
		}

		got, err := s.Monitors().Get(context.Background(), mine.ID)
		if !errors.Is(err, secretbox.ErrCannotOpen) {
			t.Fatalf("Get of the copied cipher = (%s, %v), want ErrCannotOpen", got.Config, err)
		}
	})
}

// Seeds de uma instalação anterior à cifragem: tudo em texto puro, e a
// coluna do push com o token inteiro.
const (
	legacyRedisPassword = "senha-antiga-do-redis"
	legacyPushToken     = "token-antigo-do-push-123"
	legacyDiscordToken  = "token-antigo-do-discord"
	legacySMTPPassword  = "senha-antiga-do-smtp"
	legacyHeaderToken   = "token-antigo-do-cabecalho"
)

// preSecretsVersion é a última versão antes da cifragem em repouso.
const preSecretsVersion = 6

func seedPlaintext(t *testing.T, s *sqlstore.Store) {
	t.Helper()
	if err := s.RollbackTo(preSecretsVersion); err != nil {
		t.Fatalf("RollbackTo returned unexpected error: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO monitor (name, type, target, interval_ms, timeout_ms, config, created_at, updated_at, push_token)
		 VALUES
			('cache', 'redis', 'redis:6379', 60000, 10000, '{"password":"` + legacyRedisPassword + `","db":1}', 1000, 1000, NULL),
			('cron', 'push', '', 60000, 10000, '{"token":"` + legacyPushToken + `"}', 1000, 1000, '` + legacyPushToken + `'),
			('api', 'http', 'https://exemplo.com', 60000, 10000,
			 '{"headers":{"Authorization":"` + legacyHeaderToken + `","Accept":"*/*"}}', 1000, 1000, NULL),
			('site', 'http', 'https://exemplo.com', 60000, 10000, '{"method":"GET"}', 1000, 1000, NULL)`,
		`INSERT INTO notification_channel (name, type, config, enabled, created_at, updated_at)
		 VALUES
			('discord', 'discord', '{"url":"https://discord.com/api/webhooks/1/` + legacyDiscordToken + `"}', 1, 1000, 1000),
			('email', 'email', '{"host":"smtp.exemplo.com","password":"` + legacySMTPPassword + `","from":"a@b.c","to":["d@e.f"]}', 1, 1000, 1000)`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("seeding the legacy database returned unexpected error: %v", err)
		}
	}
}

func resting(t *testing.T, s *sqlstore.Store) string {
	t.Helper()
	var all strings.Builder
	for _, q := range []string{
		`SELECT config FROM monitor ORDER BY id`,
		`SELECT config FROM notification_channel ORDER BY id`,
		`SELECT COALESCE(push_token, '') FROM monitor ORDER BY id`,
	} {
		rows, err := s.DB().Query(q)
		if err != nil {
			t.Fatalf("%s returned unexpected error: %v", q, err)
		}
		for rows.Next() {
			var v string
			_ = rows.Scan(&v)
			all.WriteString(v + "\n")
		}
		_ = rows.Close()
	}
	return all.String()
}

// Uma instalação que atualiza chega com as credenciais em texto puro. A
// primeira subida com chave as cifra, sem perder nenhuma: o checker
// continua se autenticando, o canal continua publicando, e o /push
// continua achando o dono do token. Subir de novo não muda nada — a
// migração roda em toda partida, e precisa ser idempotente.
func TestPlaintextDatabaseIsCipheredOnFirstStart(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		seedPlaintext(t, b.mustOpen(t, testKeys))

		s := b.mustOpen(t, testKeys)
		after := resting(t, s)
		for _, secret := range []string{
			legacyRedisPassword, legacyPushToken, legacyDiscordToken, legacySMTPPassword, legacyHeaderToken,
		} {
			if strings.Contains(after, secret) {
				t.Errorf("%q is still in plaintext after the upgrade:\n%s", secret, after)
			}
		}

		ctx := context.Background()
		page, err := s.Monitors().List(ctx, store.MonitorFilter{})
		if err != nil {
			t.Fatalf("List returned unexpected error: %v", err)
		}
		byName := map[string]domain.Monitor{}
		for _, m := range page.Items {
			byName[m.Name] = m
		}
		if cfg := string(byName["cache"].Config); !strings.Contains(cfg, legacyRedisPassword) || !strings.Contains(cfg, `"db":1`) {
			t.Errorf("redis config after the upgrade = %s, want the password and the rest kept", cfg)
		}
		if cfg := string(byName["api"].Config); !strings.Contains(cfg, legacyHeaderToken) || !strings.Contains(cfg, `"Accept":"*/*"`) {
			t.Errorf("http config after the upgrade = %s, want the headers kept", cfg)
		}
		if cfg := string(byName["site"].Config); cfg != `{"method":"GET"}` {
			t.Errorf("a config without secrets was rewritten: %s", cfg)
		}

		owner, err := s.Monitors().FindByPushToken(ctx, legacyPushToken)
		if err != nil || owner.ID != byName["cron"].ID || owner.PushToken != legacyPushToken {
			t.Errorf("FindByPushToken after the upgrade = (%d, %q, %v), want the cron monitor", owner.ID, owner.PushToken, err)
		}
		sum := sha256.Sum256([]byte(legacyPushToken))
		if !strings.Contains(after, hex.EncodeToString(sum[:])) {
			t.Errorf("push_token column does not hold the SHA-256 of the token:\n%s", after)
		}

		channels, err := s.Channels().List(ctx)
		if err != nil || len(channels) != 2 {
			t.Fatalf("List = (%d, %v), want the 2 channels", len(channels), err)
		}
		if !strings.Contains(string(channels[0].Config), legacyDiscordToken) ||
			!strings.Contains(string(channels[1].Config), legacySMTPPassword) {
			t.Errorf("channels after the upgrade = %s / %s, want the secrets back", channels[0].Config, channels[1].Config)
		}
		_ = s.Close()

		again := b.mustOpen(t, testKeys)
		if second := resting(t, again); second != after {
			t.Errorf("a second start rewrote the database:\nbefore %s\nafter  %s", after, second)
		}
	})
}
