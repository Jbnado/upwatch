//go:build !windows

package probe

import (
	"fmt"
	"os"
)

func isWindows() bool { return false }

// checkPrivate recusa o arquivo que outros usuários da máquina leem.
//
// Ele guarda o token de cada push e, às vezes, senha de Redis ou de
// banco. É a mesma regra do ssh com a chave privada: aberto demais é
// erro, e não aviso que ninguém lê.
func checkPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s tem permissão %o e guarda segredos; rode chmod 600 %s", path, perm, path)
	}
	return nil
}
