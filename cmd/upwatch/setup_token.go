package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
)

// setupTokenBytes é a entropia do token gerado: 128 bits, o bastante para
// chute ser inviável mesmo sem o limite por origem do POST /setup.
const setupTokenBytes = 16

// resolveSetupToken decide o token da configuração inicial.
//
// O valor de UPWATCH_SETUP_TOKEN vale como veio e não é repetido no log.
// Sem ele, a instância que ainda não tem conta gera um aleatório e o
// escreve no log uma vez, no estilo do Jupyter: quem lê o log tem acesso
// ao servidor, e quem só chegou pela porta HTTP não tem. Instância já
// configurada não gera nem menciona token, porque o assistente está
// fechado e não há o que destravar.
//
// O gerado vive só na memória: um reinício antes da configuração gera
// outro, e nada fica gravado que valha depois.
func resolveSetupToken(ctx context.Context, needsSetup func(context.Context) (bool, error), configured string, log *slog.Logger) (string, error) {
	if configured != "" {
		return configured, nil
	}

	need, err := needsSetup(ctx)
	if err != nil {
		return "", fmt.Errorf("conferindo se a instalação precisa de configuração: %w", err)
	}
	if !need {
		return "", nil
	}

	raw := make([]byte, setupTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("gerando o token de configuração: %w", err)
	}
	tok := hex.EncodeToString(raw)

	log.Warn("instalação sem conta: informe o token de configuração na tela de primeiro acesso. "+
		"Ele vale só até a conta ser criada e muda a cada reinício; para fixá-lo, defina UPWATCH_SETUP_TOKEN",
		"token_de_configuracao", tok)
	return tok, nil
}
