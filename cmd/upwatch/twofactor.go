package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

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
	slog.Warn("verificação em duas etapas desligada pela linha de comando; a conta entra só com a senha "+
		"até cadastrar o aplicativo de novo", "conta", username)
	return nil
}
