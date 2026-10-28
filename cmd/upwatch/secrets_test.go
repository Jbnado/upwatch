package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/config"
	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store/sqlstore"
)

func sqliteConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "upwatch.db")
	return cfg
}

func keyFile(t *testing.T, k secretbox.Key) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chave")
	if err := os.WriteFile(path, []byte(k.Encode()+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile returned unexpected error: %v", err)
	}
	return path
}

func generate(t *testing.T) secretbox.Key {
	t.Helper()
	k, err := secretbox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey returned unexpected error: %v", err)
	}
	return k
}

func fixed(t *testing.T, k secretbox.Key) secretbox.Source {
	t.Helper()
	box, err := secretbox.New(k)
	if err != nil {
		t.Fatalf("New returned unexpected error: %v", err)
	}
	return secretbox.Fixed(box)
}

// seedRedis grava um monitor com senha no banco, cifrado com a chave dada.
func seedRedis(t *testing.T, dsn string, k secretbox.Key) int64 {
	t.Helper()
	st, err := sqlstore.OpenSQLite(dsn, fixed(t, k))
	if err != nil {
		t.Fatalf("OpenSQLite returned unexpected error: %v", err)
	}
	defer st.Close()
	m := domain.Monitor{
		Name: "cache", Type: domain.MonitorRedis, Target: "redis:6379",
		Interval: 60e9, Timeout: 10e9, ConfirmationThreshold: 3, Enabled: true,
		Config: json.RawMessage(`{"password":"senha-do-cache"}`),
	}
	if err := st.Monitors().Create(context.Background(), &m); err != nil {
		t.Fatalf("Create returned unexpected error: %v", err)
	}
	return m.ID
}

func passwordWith(t *testing.T, dsn string, k secretbox.Key, id int64) (string, error) {
	t.Helper()
	st, err := sqlstore.OpenSQLite(dsn, fixed(t, k))
	if err != nil {
		return "", err
	}
	defer st.Close()
	m, err := st.Monitors().Get(context.Background(), id)
	if err != nil {
		return "", err
	}
	var cfg struct{ Password string }
	_ = json.Unmarshal(m.Config, &cfg)
	return cfg.Password, nil
}

// Um `docker run` sem configuração sobe: a chave nasce ao lado do banco e
// é reusada na subida seguinte.
func TestStartupWithoutKeyGeneratesOneNextToSQLite(t *testing.T) {
	cfg := sqliteConfig(t)

	st, err := openStore(cfg)
	if err != nil {
		t.Fatalf("openStore returned unexpected error: %v", err)
	}
	_ = st.Close()

	path := filepath.Join(filepath.Dir(cfg.Database.DSN), "secret.key")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no key next to the database: %v", err)
	}
	again, err := openStore(cfg)
	if err != nil {
		t.Fatalf("the second start returned unexpected error: %v", err)
	}
	_ = again.Close()
}

// Quem perdeu a chave, ou esqueceu de apontá-la, precisa ler na partida o
// que fazer, com o nome das variáveis, e não um erro de criptografia.
func TestStartupWithoutKeyOnACipheredDatabaseSaysWhatToDo(t *testing.T) {
	cfg := sqliteConfig(t)
	seedRedis(t, cfg.Database.DSN, generate(t))

	_, err := openStore(cfg)
	if !errors.Is(err, secretbox.ErrKeyMissing) {
		t.Fatalf("openStore error = %v, want ErrKeyMissing", err)
	}
	if !strings.Contains(err.Error(), "UPWATCH_SECRET_KEY_FILE") {
		t.Errorf("error %q does not name the variable to set", err)
	}
}

func TestStartupWithTheWrongKeySaysWhatToDo(t *testing.T) {
	cfg := sqliteConfig(t)
	seedRedis(t, cfg.Database.DSN, generate(t))
	cfg.SecretKey = generate(t).Encode()

	_, err := openStore(cfg)
	if !errors.Is(err, secretbox.ErrWrongKey) {
		t.Fatalf("openStore error = %v, want ErrWrongKey", err)
	}
	if !strings.Contains(err.Error(), "UPWATCH_SECRET_KEY") {
		t.Errorf("error %q does not name the variable to check", err)
	}
}

// A troca de chave pela linha de comando: as duas chaves por arquivo,
// e depois só a nova abre o banco, com a senha intacta.
func TestRotateSecretKeyCommand(t *testing.T) {
	cfg := sqliteConfig(t)
	oldKey, newKey := generate(t), generate(t)
	id := seedRedis(t, cfg.Database.DSN, oldKey)
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)

	err := rotateSecretKey([]string{"--old-key-file", keyFile(t, oldKey), "--new-key-file", keyFile(t, newKey)})
	if err != nil {
		t.Fatalf("rotate-secret-key returned unexpected error: %v", err)
	}

	if got, err := passwordWith(t, cfg.Database.DSN, newKey, id); err != nil || got != "senha-do-cache" {
		t.Errorf("with the new key: (%q, %v), want the password", got, err)
	}
	if _, err := passwordWith(t, cfg.Database.DSN, oldKey, id); !errors.Is(err, secretbox.ErrWrongKey) {
		t.Errorf("the old key still opens the database: %v", err)
	}
}

// Sem as opções, as chaves vêm do ambiente: a atual de onde a partida a
// lê, a nova de UPWATCH_NEW_SECRET_KEY, por variável ou arquivo.
func TestRotateSecretKeyReadsTheEnvironment(t *testing.T) {
	cfg := sqliteConfig(t)
	oldKey, newKey := generate(t), generate(t)
	id := seedRedis(t, cfg.Database.DSN, oldKey)
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)
	t.Setenv("UPWATCH_SECRET_KEY", oldKey.Encode())
	t.Setenv("UPWATCH_NEW_SECRET_KEY_FILE", keyFile(t, newKey))

	if err := rotateSecretKey(nil); err != nil {
		t.Fatalf("rotate-secret-key returned unexpected error: %v", err)
	}
	if got, err := passwordWith(t, cfg.Database.DSN, newKey, id); err != nil || got != "senha-do-cache" {
		t.Errorf("with the new key: (%q, %v), want the password", got, err)
	}
}

// Trocar para uma chave que não abre nada, ou sem dizer qual é a nova,
// não pode começar: a mensagem diz o que falta.
func TestRotateSecretKeyRefusesMissingOrWrongKeys(t *testing.T) {
	cfg := sqliteConfig(t)
	oldKey := generate(t)
	seedRedis(t, cfg.Database.DSN, oldKey)
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)

	err := rotateSecretKey([]string{"--old-key-file", keyFile(t, oldKey)})
	if err == nil || !strings.Contains(err.Error(), "--new-key-file") {
		t.Errorf("without a new key: %v, want the option named", err)
	}

	err = rotateSecretKey([]string{"--old-key-file", keyFile(t, generate(t)), "--new-key-file", keyFile(t, generate(t))})
	if !errors.Is(err, secretbox.ErrWrongKey) {
		t.Errorf("with a wrong old key: %v, want ErrWrongKey", err)
	}
}

// No PostgreSQL não há chave gerada: as instâncias que dividem o banco
// sorteariam cada uma a sua. Sem chave configurada, a partida para e
// explica como gerar uma.
func TestPostgresStartupWithoutKeyRefuses(t *testing.T) {
	dsn := os.Getenv("UPWATCH_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("defina UPWATCH_TEST_POSTGRES_DSN para rodar contra PostgreSQL")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open returned unexpected error: %v", err)
	}
	defer admin.Close()
	const schema = "upw_startup_sem_chave"
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE; CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("creating the schema returned unexpected error: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`) })

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	cfg := config.Default()
	cfg.Database.Driver, cfg.Database.DSN = "postgres", dsn+sep+"search_path="+schema

	_, err = openStore(cfg)
	if !errors.Is(err, secretbox.ErrKeyMissing) {
		t.Fatalf("openStore error = %v, want ErrKeyMissing", err)
	}
	if !strings.Contains(err.Error(), "openssl rand -base64 32") {
		t.Errorf("error %q does not say how to generate a key", err)
	}
}

// Chave perdida: o comando esquece as credenciais cifradas e a instalação
// volta a subir, com uma chave nova, sem perder monitores. Apagar
// credenciais não acontece por engano: sem --yes, nada muda.
func TestForgetSecretsCommand(t *testing.T) {
	cfg := sqliteConfig(t)
	id := seedRedis(t, cfg.Database.DSN, generate(t))
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)

	if err := forgetSecrets(nil); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("forget-secrets without --yes = %v, want a refusal naming --yes", err)
	}
	if _, err := openStore(cfg); !errors.Is(err, secretbox.ErrKeyMissing) {
		t.Fatalf("the refused command changed the database: %v", err)
	}

	if err := forgetSecrets([]string{"--yes"}); err != nil {
		t.Fatalf("forget-secrets --yes returned unexpected error: %v", err)
	}
	st, err := openStore(cfg)
	if err != nil {
		t.Fatalf("starting after forgetting returned unexpected error: %v", err)
	}
	defer st.Close()
	m, err := st.Monitors().Get(context.Background(), id)
	if err != nil || m.Name != "cache" || strings.Contains(string(m.Config), "enc:") {
		t.Errorf("monitor after forgetting = (%+v, %v), want it kept without the password", m, err)
	}
}
