package testdir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNewRemovesTheDirectoryAfterTheTest(t *testing.T) {
	var dir string
	t.Run("usa", func(t *testing.T) {
		dir = New(t)
		if err := os.WriteFile(filepath.Join(dir, "upwatch.db"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat after the test = %v, want the directory gone", err)
	}
}

// A CI no Linux nunca vê a falha que o t.TempDir causa no Windows; sem
// esta trava, um teste novo voltaria a usá-lo e só quem contribui pelo
// Windows descobriria, rodada sim, rodada não.
func TestNoTestUsesTempDir(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	uso := regexp.MustCompile(`\.TempDir\(\)`)

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == here || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if uso.Match(data) {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s usa TempDir(); use testdir.New(t), que o Windows consegue apagar", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
