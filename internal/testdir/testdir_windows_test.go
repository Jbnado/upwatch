package testdir

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// É a falha observada: o diretório ainda lista o arquivo cuja exclusão
// está pendente, e um instante depois está vazio.
func TestRemoveAllWaitsForAPendingDelete(t *testing.T) {
	tentativas := 0
	err := removeAll("x", func(string) error {
		tentativas++
		if tentativas < 3 {
			return &os.PathError{Op: "unlinkat", Path: "x", Err: syscall.ERROR_DIR_NOT_EMPTY}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("removeAll returned %v, want nil once the directory empties", err)
	}
	if tentativas != 3 {
		t.Errorf("remove called %d times, want 3", tentativas)
	}
}

// A espera não pode esconder um arquivo que o teste esqueceu aberto: é o
// defeito que esta limpeza existe para apontar.
func TestRemoveAllStillFailsOnAFileLeftOpen(t *testing.T) {
	dir, err := os.MkdirTemp("", "testdir")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "upwatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.Close()
		os.RemoveAll(dir)
	})

	if err := removeAll(dir, os.RemoveAll); err == nil {
		t.Fatal("removeAll succeeded with a file still open, want the error reported")
	}
}

func TestRemoveAllDoesNotRetryOtherErrors(t *testing.T) {
	tentativas := 0
	falha := fmt.Errorf("disco: %w", syscall.ERROR_PATH_NOT_FOUND)
	err := removeAll("x", func(string) error {
		tentativas++
		return falha
	})
	if err != falha || tentativas != 1 {
		t.Errorf("removeAll = %v after %d attempts, want the error at once", err, tentativas)
	}
}
