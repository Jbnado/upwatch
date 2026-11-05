package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/audit"
	"github.com/Jbnado/upwatch/internal/auth"
	"github.com/Jbnado/upwatch/internal/config"
	"github.com/Jbnado/upwatch/internal/totp"
)

const senhaDoTeste = "uma-senha-bem-longa"

// comDoisFatores cria a conta inicial com a verificação em duas etapas
// ligada, que é o estado em que o comando tem algo para desligar.
//
// Abre o banco pelo mesmo openStore do comando: assim a chave das
// credenciais é resolvida igual nos dois lados, e o teste não ensaia uma
// configuração que o comando não teria.
func comDoisFatores(t *testing.T, cfg config.Config, username string) {
	t.Helper()

	st, err := openStore(cfg)
	if err != nil {
		t.Fatalf("openStore returned unexpected error: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close returned unexpected error: %v", err)
		}
	}()

	ctx := context.Background()
	svc := auth.New(st, auth.Options{})
	u, err := svc.CreateInitialAdmin(ctx, username, senhaDoTeste)
	if err != nil {
		t.Fatalf("CreateInitialAdmin returned unexpected error: %v", err)
	}
	e, err := svc.BeginTOTP(ctx, u.ID, senhaDoTeste)
	if err != nil {
		t.Fatalf("BeginTOTP returned unexpected error: %v", err)
	}
	code, err := totp.Code(e.Secret, time.Now())
	if err != nil {
		t.Fatalf("totp.Code returned unexpected error: %v", err)
	}
	if _, err := svc.ConfirmTOTP(ctx, u.ID, "", code); err != nil {
		t.Fatalf("ConfirmTOTP returned unexpected error: %v", err)
	}
}

// capturaLogPadrão troca o logger do processo, que é o que os
// subcomandos usam, e devolve o buffer com o que o operador leria.
func capturaLogPadrão(t *testing.T) *bytes.Buffer {
	t.Helper()

	log, buf := captureLog()
	anterior := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(anterior) })
	return buf
}

// eventoDeSegurança devolve a linha de auditoria do log.
func eventoDeSegurança(t *testing.T, log string) string {
	t.Helper()

	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "evento de segurança") {
			return l
		}
	}
	t.Fatalf("nenhuma linha de evento de segurança no log:\n%s", log)
	return ""
}

// Desligar a verificação em duas etapas pela linha de comando é a ação
// mais drástica que o sistema oferece: acontece sem senha, sem código e
// sem sessão, com acesso ao banco. Fora do funil ela sai com outro
// formato, e quem audita procura "evento de segurança" e encontra tudo
// menos esta.
func TestDisableTwoFactorLogsASecurityEvent(t *testing.T) {
	cfg := sqliteConfig(t)
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)
	comDoisFatores(t, cfg, "admin")

	buf := capturaLogPadrão(t)
	if err := disableTwoFactor([]string{"admin"}); err != nil {
		t.Fatalf("disable-2fa returned unexpected error: %v", err)
	}

	linha := eventoDeSegurança(t, buf.String())
	for campo, valor := range map[string]string{
		"ação": audit.Ação2FADesligadaPelaLinhaDeComando,
		"alvo": "admin",
	} {
		if !strings.Contains(linha, campo+"="+valor) {
			t.Errorf("linha não traz %s=%s: %s", campo, valor, linha)
		}
	}

	// Não há conta autenticada nem requisição: ator e origem não têm de
	// onde sair. Ficam preenchidos de propósito, porque campo vazio num
	// coletor passa por campo ausente e a linha deixaria de casar com o
	// filtro de quem lista tudo o que mexeu no segundo fator.
	for _, campo := range []string{"ator", "origem"} {
		if !strings.Contains(linha, campo+"=") {
			t.Errorf("linha sem o campo %s: %s", campo, linha)
		}
	}
}

// Reset que não aconteceu não pode deixar linha: o log de auditoria é
// lido como prova, e uma linha a mais manda procurar um incidente que
// nunca houve.
func TestDisableTwoFactorLogsNothingWhenTheAccountIsUnknown(t *testing.T) {
	cfg := sqliteConfig(t)
	t.Setenv("UPWATCH_DB_DSN", cfg.Database.DSN)
	comDoisFatores(t, cfg, "admin")

	buf := capturaLogPadrão(t)
	if err := disableTwoFactor([]string{"nao-existe"}); err == nil {
		t.Fatal("disable-2fa accepted an account that does not exist")
	}

	if strings.Contains(buf.String(), "evento de segurança") {
		t.Errorf("registrou um reset que não aconteceu:\n%s", buf.String())
	}
}
