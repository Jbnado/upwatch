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
	"github.com/Jbnado/upwatch/internal/testdir"
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

// backend é um banco que se abre e se reabre com a chave dada.
type backend struct {
	driver, dsn string
}

func (b backend) open(t *testing.T, keys secretbox.Source) (*sqlstore.Store, error) {
	t.Helper()
	s, err := sqlstore.Open(b.driver, b.dsn, keys)
	if s != nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
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
		run(t, backend{driver: "sqlite", dsn: filepath.Join(testdir.New(t), "upwatch.db")})
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
		run(t, backend{driver: "postgres", dsn: withSearchPath(dsn, schema)})
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

		auto := filepath.Join(testdir.New(t), "secret.key")
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

// Trocar a chave recifra tudo: depois, só a nova abre o banco, e cada
// credencial continua lá. Rodar a troca de novo — porque a primeira
// pareceu travar, por exemplo — não estraga nada: a chave antiga é aceita
// para leitura enquanto a troca não acaba.
func TestRotateSecretKeyReciphersEverything(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		oldKey, newerKey := newKey(t), newKey(t)

		s := b.mustOpen(t, keysOf(oldKey))
		m := createRedis(t, s, "cache", "senha-do-cache")
		c := createNtfy(t, s, "token-do-ntfy")
		before := resting(t, s)

		if err := s.RotateSecretKey(context.Background(), newerKey); err != nil {
			t.Fatalf("RotateSecretKey returned unexpected error: %v", err)
		}
		_ = s.Close()

		if _, err := b.open(t, keysOf(oldKey)); !errors.Is(err, secretbox.ErrWrongKey) {
			t.Errorf("the old key still opens the database after rotation: %v", err)
		}

		// Uma segunda troca para a mesma chave, com a antiga ainda à mão.
		again := b.mustOpen(t, keysOf(oldKey, newerKey))
		if err := again.RotateSecretKey(context.Background(), newerKey); err != nil {
			t.Fatalf("running the rotation again returned unexpected error: %v", err)
		}
		_ = again.Close()

		rotated := b.mustOpen(t, keysOf(newerKey))
		after := resting(t, rotated)
		if after == before {
			t.Error("the stored values did not change with the rotation")
		}
		for _, secret := range []string{"senha-do-cache", "token-do-ntfy"} {
			if strings.Contains(after, secret) {
				t.Errorf("%q is in plaintext after the rotation", secret)
			}
		}
		gotM, err := rotated.Monitors().Get(context.Background(), m.ID)
		if err != nil || !strings.Contains(string(gotM.Config), "senha-do-cache") {
			t.Errorf("monitor after rotation = (%s, %v), want the password back", gotM.Config, err)
		}
		gotC, err := rotated.Channels().Get(context.Background(), c.ID)
		if err != nil || !strings.Contains(string(gotC.Config), "token-do-ntfy") {
			t.Errorf("channel after rotation = (%s, %v), want the token back", gotC.Config, err)
		}
	})
}

// Uma chave nova que seja a própria antiga não troca nada, e quem a pediu
// provavelmente apontou o arquivo errado.
func TestRotateSecretKeyRefusesTheSameKey(t *testing.T) {
	s := newStore(t).(*sqlstore.Store)
	if err := s.RotateSecretKey(context.Background(), testKey); err == nil {
		t.Error("rotating to the key in use was accepted")
	}
}

// Chave perdida não tem volta: as credenciais cifradas com ela se foram.
// O resto do banco não pode ir junto. Esquecer as credenciais apaga só os
// valores cifrados e o sentinela; a instalação volta a subir com uma
// chave nova, monitores e canais no lugar, e a credencial é digitada de
// novo pela interface. O token do push sai da busca também: um hash sem
// o token correspondente na configuração faria o /push aceitar um token
// que a interface não mostra mais.
func TestForgetSecretsRecoversFromALostKey(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		lost := b.mustOpen(t, keysOf(newKey(t)))
		redis := createRedis(t, lost, "cache", "senha-perdida")
		channel := createNtfy(t, lost, "token-perdido")
		push := domain.Monitor{
			Name: "cron", Type: domain.MonitorPush, Interval: 60e9, Timeout: 10e9, ConfirmationThreshold: 3,
			Enabled: true, Config: json.RawMessage(`{"token":"token-do-push-perdido"}`), PushToken: "token-do-push-perdido",
		}
		if err := lost.Monitors().Create(ctx, &push); err != nil {
			t.Fatalf("Create returned unexpected error: %v", err)
		}
		_ = lost.Close()

		n, err := sqlstore.ForgetSecrets(b.driver, b.dsn)
		if err != nil {
			t.Fatalf("ForgetSecrets returned unexpected error: %v", err)
		}
		if n != 3 {
			t.Errorf("ForgetSecrets erased %d values, want 3", n)
		}

		s := b.mustOpen(t, keysOf(newKey(t)))
		got, err := s.Monitors().Get(ctx, redis.ID)
		if err != nil {
			t.Fatalf("Get returned unexpected error: %v", err)
		}
		if got.Name != "cache" || got.Target != "redis:6379" || strings.Contains(string(got.Config), "enc:") {
			t.Errorf("monitor after forgetting = %+v (%s), want it kept without the password", got, got.Config)
		}
		gotChannel, err := s.Channels().Get(ctx, channel.ID)
		if err != nil || !strings.Contains(string(gotChannel.Config), `"topic":"plantao"`) || strings.Contains(string(gotChannel.Config), "enc:") {
			t.Errorf("channel after forgetting = (%s, %v), want it kept without the token", gotChannel.Config, err)
		}
		if _, err := s.Monitors().FindByPushToken(ctx, "token-do-push-perdido"); err == nil {
			t.Error("the forgotten push token still finds its monitor")
		}

		// A credencial digitada de novo é cifrada com a chave nova.
		got.Config = json.RawMessage(`{"password":"senha-nova"}`)
		if err := s.Monitors().Update(ctx, got); err != nil {
			t.Fatalf("Update returned unexpected error: %v", err)
		}
		if again, err := s.Monitors().Get(ctx, redis.ID); err != nil || !strings.Contains(string(again.Config), "senha-nova") {
			t.Errorf("monitor after typing the password again = (%s, %v)", again.Config, err)
		}
	})
}
