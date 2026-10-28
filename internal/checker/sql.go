package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// MaxSQLQueryLength limita o tamanho da consulta de verificação. Consulta
// de monitor é uma linha; algo maior que isto é relatório, e relatório
// rodando a cada minuto vira carga no banco que se queria vigiar.
const MaxSQLQueryLength = 4096

// defaultSQLQuery é a consulta quando nenhuma é configurada: prova que o
// banco aceita sessão e executa, sem tocar em tabela nenhuma.
const defaultSQLQuery = "SELECT 1"

// sqlConfig reúne o que PostgreSQL e MySQL têm em comum.
//
// O alvo fica no Target (host:porta), não numa cadeia de conexão: o
// endereço aparece no painel e na página pública, e uma DSN carrega a
// senha junto.
type sqlConfig struct {
	Username string `json:"username"`
	// Password é segredo: não volta na API (ver RedactConfig).
	Password string `json:"password,omitempty"`
	Database string `json:"database,omitempty"`
	// Query é executada numa transação somente leitura. Vazia usa SELECT 1.
	Query string `json:"query,omitempty"`
}

func (c sqlConfig) validate(kind string) error {
	if strings.TrimSpace(c.Username) == "" {
		return fmt.Errorf("checker: o usuário do %s não pode ser vazio", kind)
	}
	if c.Query != "" && strings.TrimSpace(c.Query) == "" {
		return errors.New("checker: a consulta não pode ser só espaço; deixe vazia para usar SELECT 1")
	}
	if len(c.Query) > MaxSQLQueryLength {
		return fmt.Errorf("checker: a consulta passa de %d caracteres", MaxSQLQueryLength)
	}
	return nil
}

func (c sqlConfig) query() string {
	if c.Query == "" {
		return defaultSQLQuery
	}
	return c.Query
}

// serverLimit é quanto a consulta pode durar no servidor: um décimo abaixo
// do que resta do timeout do monitor.
//
// O timeout do lado do UpWatch só fecha o socket. Sem um limite combinado
// com o próprio servidor, uma consulta lenta seguiria rodando lá depois de
// a verificação desistir, e a cada intervalo o monitor somaria mais uma
// sessão viva até esgotar as conexões do banco que ele devia vigiar. A
// folga faz o servidor desistir primeiro e responder com o motivo.
func serverLimit(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline)
	return max(remaining-remaining/10, time.Millisecond)
}

// sqlFailure monta o resultado de uma falha de banco.
//
// A causa vai para o painel, para o aviso no canal e para o histórico
// exportado, lidos por quem não tem acesso ao banco. Por isso ela diz a
// etapa, a classe da falha e o código, nunca o texto do driver ou do
// servidor: esse texto repete o que recebeu (a senha, ecoada por um proxy)
// e o que leu (o valor de uma linha num erro de conversão), e "SELECT
// CAST(token AS int) FROM api_keys" leria o token pela causa. O texto
// inteiro vai para o log do servidor, que só o operador lê.
//
// classify traduz os erros próprios de cada banco; o que é comum aos dois
// (tempo, rede, TLS) se resolve aqui.
func sqlFailure(m domain.Monitor, meta map[string]string, password, stage string, err error, classify func(error) (string, bool)) Result {
	class, known := classify(err)
	if !known {
		class, known = commonSQLClass(err)
	}
	if !known {
		class = "falhou (detalhe no log do servidor)"
	}

	// Classe conhecida e fora da consulta (recusa, timeout, senha) se
	// repete a cada intervalo enquanto o banco estiver fora e a causa já
	// diz o bastante; no nível de aviso fica o que só o texto explica.
	level := slog.LevelDebug
	if !known || stage == "consulta" {
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "checker: falha na verificação de banco",
		"monitor", m.Name, "id", m.ID, "tipo", m.Type.String(), "etapa", stage,
		"erro", scrubSecret(err.Error(), password))

	return Result{Status: domain.StatusDown, Message: stage + ": " + class, Meta: meta}
}

// commonSQLClass classifica as falhas que não vêm do servidor de banco.
// O texto do sistema operacional é local e pode aparecer; o do servidor,
// não.
func commonSQLClass(err error) (string, bool) {
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "tempo esgotado", true
	case errors.As(err, &dnsErr):
		return "o nome do servidor não resolve", true
	case errors.As(err, &certErr), errors.As(err, &hostErr), errors.As(err, &authErr):
		return "o certificado TLS do servidor não foi aceito", true
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "não foi possível conectar (" + opErr.Err.Error() + ")", true
	}
	return "", false
}

// splitTarget confere o formato host:porta do alvo.
func splitTarget(target string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port == "" {
		return "", "", false
	}
	return host, port, true
}
