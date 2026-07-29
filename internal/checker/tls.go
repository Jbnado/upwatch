package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// DefaultDegradedDaysBefore é quantos dias antes do vencimento o
// certificado passa a ser reportado como degradado.
//
// Quatorze dias dão folga sobre a renovação automática do Let's Encrypt,
// que ocorre aos trinta, de modo que o alerta só apareça quando a
// renovação realmente falhou.
const DefaultDegradedDaysBefore = 14

// TLSConfig é a configuração de um monitor de certificado.
type TLSConfig struct {
	// DegradedDaysBefore é a antecedência do aviso de vencimento.
	// Zero usa o padrão; negativo é recusado na validação.
	DegradedDaysBefore int `json:"degraded_days_before,omitempty"`

	// SkipVerify ignora a validação da cadeia, mas nunca a de expiração —
	// vigiar o vencimento é a razão de o monitor existir.
	SkipVerify bool `json:"skip_verify,omitempty"`

	// ServerName sobrescreve o SNI, para PKI interna que emite
	// certificados para nomes que só o DNS interno resolve.
	ServerName string `json:"server_name,omitempty"`

	// RootCAPEM confia numa autoridade própria, sem precisar instalá-la no
	// sistema operacional do contêiner.
	RootCAPEM string `json:"root_ca_pem,omitempty"`
}

func (c TLSConfig) degradedWindow() time.Duration {
	days := c.DegradedDaysBefore
	if days == 0 {
		days = DefaultDegradedDaysBefore
	}
	return time.Duration(days) * 24 * time.Hour
}

// TLSChecker inspeciona o certificado apresentado por um serviço.
type TLSChecker struct {
	dialer *net.Dialer
}

// NewTLS cria o checker de certificado.
func NewTLS() *TLSChecker {
	return &TLSChecker{dialer: &net.Dialer{}}
}

// Type identifica o tipo de monitor atendido.
func (c *TLSChecker) Type() domain.MonitorType { return domain.MonitorTLS }

// ValidateConfig confere a configuração no cadastro.
func (c *TLSChecker) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parseTLSConfig(raw)
	if err != nil {
		return err
	}
	if cfg.DegradedDaysBefore < 0 {
		return fmt.Errorf("checker: degraded_days_before não pode ser negativo")
	}
	if cfg.RootCAPEM != "" {
		if _, err := rootPool(cfg.RootCAPEM); err != nil {
			return err
		}
	}
	return nil
}

// Check inspeciona o certificado e classifica o que encontrou.
//
// O aperto de mão é feito sem verificação e a validação acontece depois,
// manualmente. Deixar o handshake reprovar devolveria um erro genérico;
// separando as etapas o monitor consegue dizer se o problema é expiração,
// cadeia ou nome — que são causas diferentes, com correções diferentes.
func (c *TLSChecker) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseTLSConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	host, port, err := net.SplitHostPort(m.Target)
	if err != nil || port == "" {
		return down("alvo precisa estar no formato host:porta, recebido %q", m.Target)
	}

	serverName := cfg.ServerName
	if serverName == "" {
		serverName = host
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	start := time.Now()
	conn, err := tls.DialWithDialer(
		dialerWithDeadline(c.dialer, ctx),
		"tcp", m.Target,
		&tls.Config{
			// Verificação desligada de propósito: a validação é feita
			// abaixo, para o diagnóstico ser específico.
			InsecureSkipVerify: true, //nolint:gosec
			ServerName:         serverName,
			MinVersion:         tls.VersionTLS12,
		},
	)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return down("%v", err)
	}
	defer conn.Close()

	chain := conn.ConnectionState().PeerCertificates
	if len(chain) == 0 {
		return down("o servidor não apresentou certificado")
	}
	leaf := chain[0]

	res := Result{
		LatencyMS: latency,
		Meta: map[string]string{
			"subject":           leaf.Subject.CommonName,
			"issuer":            leaf.Issuer.CommonName,
			"not_after":         leaf.NotAfter.UTC().Format(time.RFC3339),
			"days_until_expiry": strconv.Itoa(daysUntil(leaf.NotAfter)),
		},
	}

	now := time.Now()
	switch {
	case now.After(leaf.NotAfter):
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("certificado expirou em %s",
			leaf.NotAfter.UTC().Format(time.RFC3339))
		return res
	case now.Before(leaf.NotBefore):
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("certificado só passa a valer em %s",
			leaf.NotBefore.UTC().Format(time.RFC3339))
		return res
	}

	if !cfg.SkipVerify {
		if msg := verifyChain(chain, serverName, cfg.RootCAPEM); msg != "" {
			res.Status = domain.StatusDown
			res.Message = msg
			return res
		}
	}

	if remaining := time.Until(leaf.NotAfter); remaining < cfg.degradedWindow() {
		res.Status = domain.StatusDegraded
		res.Message = fmt.Sprintf("certificado expira em %d dias", daysUntil(leaf.NotAfter))
		return res
	}

	res.Status = domain.StatusUp
	return res
}

// verifyChain valida cadeia e nome, devolvendo a causa específica da
// reprovação ou string vazia quando tudo confere.
func verifyChain(chain []*x509.Certificate, serverName, rootCAPEM string) string {
	roots, err := rootPool(rootCAPEM)
	if err != nil {
		return err.Error()
	}

	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}

	// Cadeia e nome são verificados em passos separados para a mensagem
	// apontar a causa certa: CA desconhecida e certificado emitido para
	// outro nome exigem correções completamente diferentes.
	if _, err := chain[0].Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
	}); err != nil {
		return fmt.Sprintf("cadeia de certificação inválida: %v", err)
	}
	if err := chain[0].VerifyHostname(serverName); err != nil {
		return fmt.Sprintf("certificado não vale para o nome %q: %v", serverName, err)
	}
	return ""
}

// rootPool monta o conjunto de autoridades confiáveis.
//
// Sem CA informada usa as do sistema; com CA informada usa só ela, de modo
// que um serviço de PKI interna não passe a aceitar qualquer autoridade
// pública por engano.
func rootPool(pemData string) (*x509.CertPool, error) {
	if pemData == "" {
		return nil, nil // nil delega às raízes do sistema
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pemData)) {
		return nil, fmt.Errorf("checker: root_ca_pem não contém certificado válido")
	}
	return pool, nil
}

// dialerWithDeadline traduz o prazo do contexto para o dialer, já que
// tls.DialWithDialer não recebe contexto.
func dialerWithDeadline(base *net.Dialer, ctx context.Context) *net.Dialer {
	d := *base
	if deadline, ok := ctx.Deadline(); ok {
		d.Deadline = deadline
	}
	return &d
}

// daysUntil conta dias inteiros até t, sem arredondar para cima.
func daysUntil(t time.Time) int {
	return int(time.Until(t).Hours() / 24)
}

func parseTLSConfig(raw json.RawMessage) (TLSConfig, error) {
	var cfg TLSConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração TLS inválida: %w", err)
	}
	return cfg, nil
}
