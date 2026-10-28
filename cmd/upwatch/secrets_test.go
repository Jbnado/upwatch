package main

import (
	"context"
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
