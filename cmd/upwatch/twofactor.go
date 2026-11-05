package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/Jbnado/upwatch/internal/audit"
	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/config"
)

// disableTwoFactor é o comando `upwatch disable-2fa <conta>`: desliga a
// verificação em duas etapas de uma conta e encerra as sessões dela.
//
// É a saída de quando não sobra ninguém para fazer isso pela interface —
// o único administrador perdeu o celular e os códigos de recuperação.
// Quem roda tem acesso ao servidor e ao banco, que é prova bastante.
func disableTwoFactor(args []string) error {
	fs := flag.NewFlagSet("disable-2fa", flag.ContinueOnError)
	configPath := fs.String("config", os.Getenv("UPWATCH_CONFIG"), "caminho do arquivo de configuração (opcional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("disable-2fa: informe a conta, como em `upwatch disable-2fa admin`")
	}
	username := fs.Arg(0)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			slog.Error("falha ao fechar o banco", "erro", err)
		}
	}()

	if err := auth.New(st, auth.Options{}).ResetTOTPByUsername(context.Background(), username); err != nil {
		return fmt.Errorf("disable-2fa: %w", err)
	}

	// Pelo mesmo funil do servidor, e não por um slog.Warn solto: esta é
	// a ação mais drástica que o sistema oferece — desliga o segundo fator
	// sem senha, sem código e sem sessão — e quem audita a procura onde
	// procura todas as outras. Fora do funil ela sai com outros campos, e
	// o filtro que encontra o resto passa reto justamente por ela.
	//
	// Não há conta autenticada nem requisição, então ator e origem não têm
	// de onde sair. Ficam preenchidos porque campo vazio num coletor passa
	// por campo ausente, e a linha deixaria de casar com o filtro.
	//
	// Depois dela vem o aviso em prosa, que é para a pessoa no terminal:
	// a linha de auditoria tem campos fixos e não cabe explicar nela o que
	// acontece a seguir.
	audit.Registra(slog.Default(), audit.Evento{
		Ator:   audit.AtorLinhaDeComando,
		Ação:   audit.Ação2FADesligadaPelaLinhaDeComando,
		Alvo:   username,
		Origem: audit.OrigemLocal,
	})
	slog.Warn("verificação em duas etapas desligada pela linha de comando; a conta entra só com a senha "+
		"até cadastrar o aplicativo de novo", "conta", username)
	return nil
}
