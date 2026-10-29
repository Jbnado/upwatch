package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// needs devolve uma consulta de "precisa de configuração" com resposta fixa.
func needs(v bool) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return v, nil }
}

// captureLog devolve um logger que escreve num buffer, para o teste ler o
// que o operador leria.
func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// Sem variável, a instância nova gera o token e o mostra no log, uma vez.
// É o padrão seguro sem passo extra: quem lê o log tem acesso ao servidor,
// e quem só chegou pela porta HTTP não tem.
func TestSetupTokenIsGeneratedAndLoggedOnceWhenSetupIsPending(t *testing.T) {
	log, buf := captureLog()

	tok, err := resolveSetupToken(context.Background(), needs(true), "", log)
	if err != nil {
		t.Fatalf("resolveSetupToken returned unexpected error: %v", err)
	}

	// 128 bits ou mais: o limite por origem ajuda, mas o token gerado não
	// deve depender dele para resistir a chute.
	if len(tok) < 32 {
		t.Errorf("generated token %q is too short to resist guessing", tok)
	}
	if n := strings.Count(buf.String(), tok); n != 1 {
		t.Errorf("the token appears %d times in the log, want exactly once:\n%s", n, buf.String())
	}
}

// Cada subida gera outro: um token fixo no binário seria o mesmo em toda
// instalação, e servir de chave para todas.
func TestGeneratedSetupTokenChangesEveryStart(t *testing.T) {
	log, _ := captureLog()

	a, err := resolveSetupToken(context.Background(), needs(true), "", log)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resolveSetupToken(context.Background(), needs(true), "", log)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two starts generated the same token %q", a)
	}
}

// O valor escolhido pelo operador é usado como veio e não vai para o log:
// ele já sabe qual é, e o log costuma ser lido por mais gente do que o
// arquivo de segredo.
func TestConfiguredSetupTokenIsUsedAndNotLogged(t *testing.T) {
	log, buf := captureLog()
	const configured = "token-de-configuracao-dos-testes"

	tok, err := resolveSetupToken(context.Background(), needs(true), configured, log)
	if err != nil {
		t.Fatalf("resolveSetupToken returned unexpected error: %v", err)
	}
	if tok != configured {
		t.Errorf("token = %q, want the configured %q", tok, configured)
	}
	if strings.Contains(buf.String(), configured) {
		t.Errorf("the configured token leaked into the log:\n%s", buf.String())
	}
}

// Instância já configurada (a VPS que já tem conta) não gera token nem
// escreve nada sobre ele: o assistente está fechado, e um token no log
// sugeriria haver algo a destravar.
func TestNoSetupTokenOnceTheAccountExists(t *testing.T) {
	log, buf := captureLog()

	tok, err := resolveSetupToken(context.Background(), needs(false), "", log)
	if err != nil {
		t.Fatalf("resolveSetupToken returned unexpected error: %v", err)
	}
	if tok != "" {
		t.Errorf("token = %q on a configured instance, want none", tok)
	}
	if buf.Len() != 0 {
		t.Errorf("a configured instance logged about the setup token:\n%s", buf.String())
	}
}

// Sem conseguir consultar o banco não há como saber se o assistente está
// aberto; subir assim esconderia o defeito até alguém tentar entrar.
func TestSetupTokenFailsWhenTheStoreCannotAnswer(t *testing.T) {
	log, _ := captureLog()
	falha := errors.New("banco fora")

	_, err := resolveSetupToken(context.Background(),
		func(context.Context) (bool, error) { return false, falha }, "", log)
	if !errors.Is(err, falha) {
		t.Errorf("err = %v, want the store error", err)
	}
}
