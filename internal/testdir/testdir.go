// Package testdir dá aos testes um diretório temporário que o Windows
// consegue apagar no fim.
//
// O t.TempDir falha de vez em quando no Windows com "a pasta não está
// vazia", mesmo com tudo do teste fechado: um filtro de sistema de
// arquivos (antivírus, indexador) abre o arquivo recém-gravado para
// inspecionar e, enquanto o segura, a exclusão fica pendente e o nome
// continua no diretório por um ou dois milissegundos. O testing já
// repete a remoção quando o próprio arquivo está preso, mas não quando é
// o diretório que ainda não esvaziou, e o teste fica vermelho sem defeito
// nenhum. Reproduz-se com um programa que só grava, fecha e apaga, sem
// nada do UpWatch no meio.
package testdir

import (
	"os"
	"strings"
	"testing"
	"time"
)

// prazo é quanto se insiste antes de dar a remoção por falha. O mesmo do
// testing: muito acima do que o filtro leva para soltar o arquivo, e
// curto o bastante para um arquivo realmente esquecido aberto aparecer.
const prazo = 2 * time.Second

// New cria um diretório temporário exclusivo do teste, apagado quando o
// teste e seus Cleanups registrados depois desta chamada terminam.
//
// Substitui o t.TempDir em todo o repositório. Um arquivo que o próprio
// teste esqueceu aberto continua falhando: ele segue preso por todo o
// prazo, e não por milissegundos.
func New(t testing.TB) string {
	t.Helper()

	dir, err := os.MkdirTemp("", pattern(t.Name()))
	if err != nil {
		t.Fatalf("MkdirTemp returned unexpected error: %v", err)
	}
	t.Cleanup(func() {
		if err := removeAll(dir, os.RemoveAll); err != nil {
			t.Errorf("removendo o diretório temporário: %v", err)
		}
	})
	return dir
}

// pattern deriva o prefixo do nome do teste, como o t.TempDir, para quem
// for olhar a pasta temporária saber de que teste ela é.
func pattern(name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < ' ' {
			return -1
		}
		return r
	}, name)
	return name[:min(len(name), 64)]
}

func removeAll(dir string, remove func(string) error) error {
	inicio := time.Now()
	for {
		err := remove(dir)
		if err == nil || !retryable(err) || time.Since(inicio) >= prazo {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}
