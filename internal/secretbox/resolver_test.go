package secretbox_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/testdir"
)

func newResolver(t *testing.T, configured string) (secretbox.Resolver, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	return secretbox.Resolver{
		Configured: configured,
		AutoPath:   filepath.Join(testdir.New(t), "secret.key"),
		Logger:     slog.New(slog.NewTextHandler(&logs, nil)),
	}, &logs
}

// sealedWith confere que a caixa devolvida é a da chave esperada.
func sealedWith(t *testing.T, box *secretbox.Box, key secretbox.Key) bool {
	t.Helper()
	v, err := box.Seal("x", redisSlot)
	if err != nil {
		t.Fatalf("Seal returned unexpected error: %v", err)
	}
	_, err = mustBox(t, key).Open(v, redisSlot)
	return err == nil
}

func TestResolverUsesTheConfiguredKey(t *testing.T) {
	key := mustKey(t)
	r, _ := newResolver(t, key.Encode())

	box, err := r.Box(true)
	if err != nil {
		t.Fatalf("Box returned unexpected error: %v", err)
	}
	if !sealedWith(t, box, key) {
		t.Error("the box does not use the configured key")
	}
	if _, err := os.Stat(r.AutoPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a key was written next to the database although one was configured: %v", err)
	}
}

// Um `docker run` sem configuração nenhuma precisa subir. A chave gerada
// fica ao lado do banco, legível só pelo dono, e o log avisa uma vez que
// ali ela protege pouco: quem leva o volume leva a chave junto.
func TestResolverGeneratesAKeyForANewInstallation(t *testing.T) {
	r, logs := newResolver(t, "")

	box, err := r.Box(false)
	if err != nil {
		t.Fatalf("Box returned unexpected error: %v", err)
	}
	data, err := os.ReadFile(r.AutoPath)
	if err != nil {
		t.Fatalf("the generated key was not written: %v", err)
	}
	key, err := secretbox.ParseKey(string(data))
	if err != nil {
		t.Fatalf("the generated key file does not parse: %v", err)
	}
	if !sealedWith(t, box, key) {
		t.Error("the box does not use the key written to disk")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(r.AutoPath)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("key file permission = %o, want 600", perm)
		}
	}
	if !strings.Contains(logs.String(), r.AutoPath) || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("no warning naming where the key was written: %s", logs.String())
	}

	// Na subida seguinte, a mesma chave, e sem repetir o aviso.
	logs.Reset()
	again, err := r.Box(true)
	if err != nil {
		t.Fatalf("Box on the second start returned unexpected error: %v", err)
	}
	if !sealedWith(t, again, key) {
		t.Error("the second start did not reuse the generated key")
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the generation warning was repeated: %s", logs.String())
	}
}

// Um banco que já tem segredo cifrado e nenhuma chave à vista perdeu a
// chave, ou a pessoa esqueceu de apontá-la. Gerar uma nova aqui faria o
// UpWatch subir sem conseguir abrir senha nenhuma — todo monitor com
// credencial passaria a falhar, e o aviso viria como queda dos serviços.
func TestResolverRefusesASealedDatabaseWithoutKey(t *testing.T) {
	r, _ := newResolver(t, "")

	_, err := r.Box(true)
	if !errors.Is(err, secretbox.ErrKeyMissing) {
		t.Fatalf("Box(sealed) error = %v, want ErrKeyMissing", err)
	}
	if _, statErr := os.Stat(r.AutoPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a new key was generated for a database that already has one: %v", statErr)
	}
}

// Sem lugar para guardar a chave gerada — o PostgreSQL, que roda em mais
// de uma instância —, não se gera nada: cada instância sortearia a sua, e
// uma não leria o que a outra gravou.
func TestResolverWithoutAutoPathRequiresAKey(t *testing.T) {
	r, _ := newResolver(t, "")
	r.AutoPath = ""

	if _, err := r.Box(false); !errors.Is(err, secretbox.ErrKeyMissing) {
		t.Errorf("Box without a place to keep the key error = %v, want ErrKeyMissing", err)
	}
}

func TestResolverRefusesAnInvalidConfiguredKey(t *testing.T) {
	r, _ := newResolver(t, "curta")
	if _, err := r.Box(false); err == nil || errors.Is(err, secretbox.ErrKeyMissing) {
		t.Errorf("Box with an invalid key error = %v, want a parse error", err)
	}
}

// Quem seguiu o conselho e levou a chave para fora do volume, mas deixou
// a gerada lá, continua com a chave ao lado do banco. O log aponta isso.
func TestResolverWarnsAboutAStaleGeneratedKey(t *testing.T) {
	key := mustKey(t)
	r, logs := newResolver(t, key.Encode())
	if err := os.WriteFile(r.AutoPath, []byte(key.Encode()), 0o600); err != nil {
		t.Fatalf("WriteFile returned unexpected error: %v", err)
	}

	if _, err := r.Box(true); err != nil {
		t.Fatalf("Box returned unexpected error: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), r.AutoPath) {
		t.Errorf("no warning about the key left next to the database: %s", logs.String())
	}
}
