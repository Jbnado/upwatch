package checker_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// authority é uma CA de teste capaz de emitir certificados com validade
// controlada, que é o que permite exercitar expiração sem esperar meses.
type authority struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pemDER []byte
}

func newAuthority(t *testing.T) *authority {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned unexpected error: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "UpWatch Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate returned unexpected error: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate returned unexpected error: %v", err)
	}
	return &authority{cert: cert, key: key, pemDER: der}
}

// pemBytes devolve a CA no formato PEM, como o operador colaria na config.
func (a *authority) pemBytes() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.pemDER}))
}

// issue emite um certificado de servidor para hosts, válido até notAfter.
func (a *authority) issue(t *testing.T, hosts []string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey returned unexpected error: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			continue
		}
		tmpl.DNSNames = append(tmpl.DNSNames, h)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatalf("CreateCertificate returned unexpected error: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveTLS sobe um listener TLS local com o certificado dado e devolve o
// endereço. Porta arbitrária de propósito: certificado não é assunto
// exclusivo da 443.
func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("tls.Listen returned unexpected error: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Força o handshake e encerra; o checker só precisa do certificado.
			if tc, ok := conn.(*tls.Conn); ok {
				_ = tc.HandshakeContext(context.Background())
			}
			conn.Close()
		}
	}()
	return ln.Addr().String()
}

func tlsMonitor(t *testing.T, target string, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage("{}")
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}
	return domain.Monitor{
		ID: 1, Name: "certificado", Type: domain.MonitorTLS, Target: target,
		Interval: time.Minute, Timeout: 5 * time.Second,
		ConfirmationThreshold: 1, Config: raw,
	}
}

func checkTLS(t *testing.T, target string, cfg map[string]any) checker.Result {
	t.Helper()
	return checker.NewTLS().Check(context.Background(), tlsMonitor(t, target, cfg))
}

func TestTLSTypeIsTLS(t *testing.T) {
	if got := checker.NewTLS().Type(); got != domain.MonitorTLS {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorTLS)
	}
}

func TestTLSValidCertificateIsUp(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Avisar antes de vencer é o ponto do monitor: descobrir na expiração já é
// tarde, o serviço já caiu.
func TestTLSCertificateNearingExpiryIsDegraded(t *testing.T) {
	ca := newAuthority(t)
	// A folga de uma hora evita que o teste dependa dos microssegundos
	// entre emitir o certificado e verificá-lo.
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(3*24*time.Hour+time.Hour)))

	got := checkTLS(t, addr, map[string]any{
		"root_ca_pem":          ca.pemBytes(),
		"degraded_days_before": 14,
	})

	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDegraded)
	}
	if !strings.Contains(got.Message, "3 dias") {
		t.Errorf("Message = %q, want it to state how many days are left", got.Message)
	}
}

// A contagem trunca em vez de arredondar para cima: um certificado com
// meia hora de vida precisa aparecer como zero dia, não como um. Nunca
// superestimar o tempo restante é a propriedade segura num aviso.
func TestTLSDaysRemainingNeverOverstates(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(30*time.Minute)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	if got.Meta["days_until_expiry"] != "0" {
		t.Errorf("days_until_expiry = %q, want %q for a certificate with 30 minutes left",
			got.Meta["days_until_expiry"], "0")
	}
	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDegraded)
	}
}

func TestTLSCertificateOutsideDegradedWindowIsUp(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(60*24*time.Hour)))

	got := checkTLS(t, addr, map[string]any{
		"root_ca_pem":          ca.pemBytes(),
		"degraded_days_before": 14,
	})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestTLSExpiredCertificateIsDown(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "expir") {
		t.Errorf("Message = %q, want it to name expiry as the cause", got.Message)
	}
}

func TestTLSCertificateNotYetValidIsDown(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Certificado assinado por CA desconhecida precisa reprovar, e a mensagem
// precisa dizer que o problema é a cadeia — não a expiração.
func TestTLSUntrustedChainIsDown(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour)))

	// Sem informar a CA, ela é desconhecida para o sistema.
	got := checkTLS(t, addr, nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "cadeia") {
		t.Errorf("Message = %q, want it to point at the certificate chain", got.Message)
	}
}

// Certificado válido emitido para outro nome é falha de configuração
// comum, e confundi-la com cadeia inválida manda o operador para o
// caminho errado.
func TestTLSHostnameMismatchIsDown(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"outro-servico.exemplo"},
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "nome") {
		t.Errorf("Message = %q, want it to point at the hostname mismatch", got.Message)
	}
}

// PKI interna emite para nomes que só o DNS interno resolve; o SNI
// precisa ser configurável para o certificado certo ser apresentado.
func TestTLSHonoursServerNameOverride(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"servico.interno"},
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour)))

	got := checkTLS(t, addr, map[string]any{
		"root_ca_pem": ca.pemBytes(),
		"server_name": "servico.interno",
	})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Ainda que a cadeia seja ignorada, a expiração continua sendo verificada:
// é o que o monitor existe para vigiar.
func TestTLSSkipVerifyStillReportsExpiry(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)))

	got := checkTLS(t, addr, map[string]any{"skip_verify": true})

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v: expiry must be checked even without chain verification",
			got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "expir") {
		t.Errorf("Message = %q, want it to name expiry", got.Message)
	}
}

func TestTLSSkipVerifyAcceptsUntrustedChain(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(365*24*time.Hour)))

	got := checkTLS(t, addr, map[string]any{"skip_verify": true})

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestTLSReportsDaysUntilExpiryInMeta(t *testing.T) {
	ca := newAuthority(t)
	addr := serveTLS(t, ca.issue(t, []string{"127.0.0.1"},
		time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour+time.Hour)))

	got := checkTLS(t, addr, map[string]any{"root_ca_pem": ca.pemBytes()})

	days, err := strconv.Atoi(got.Meta["days_until_expiry"])
	if err != nil {
		t.Fatalf("Meta[days_until_expiry] = %q, want a number", got.Meta["days_until_expiry"])
	}
	if days != 30 {
		t.Errorf("days_until_expiry = %d, want 30", days)
	}
	if got.Meta["issuer"] == "" {
		t.Error("Meta[issuer] is empty, want the issuing authority")
	}
	if got.Meta["not_after"] == "" {
		t.Error("Meta[not_after] is empty, want the expiry date")
	}
}

func TestTLSClosedPortIsDown(t *testing.T) {
	got := checkTLS(t, closedPort(t), nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Apontar um monitor TLS para uma porta em texto puro é engano comum; o
// handshake fica pendurado até o timeout em vez de falhar de imediato.
func TestTLSPlainTextPortIsDown(t *testing.T) {
	addr := listen(t)
	m := tlsMonitor(t, addr, nil)
	m.Timeout = 300 * time.Millisecond

	got := checker.NewTLS().Check(context.Background(), m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

func TestTLSTargetWithoutPortIsDown(t *testing.T) {
	got := checkTLS(t, "example.com", nil)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "porta") {
		t.Errorf("Message = %q, want it to point at the missing port", got.Message)
	}
}

func TestTLSValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := checker.NewTLS().ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

func TestTLSValidateConfigRejectsUnparseableRootCA(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"root_ca_pem": "isto não é um certificado"})

	if err := checker.NewTLS().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a bad root CA returned nil error, want an error")
	}
}

func TestTLSValidateConfigRejectsNegativeDegradedWindow(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"degraded_days_before": -1})

	if err := checker.NewTLS().ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a negative window returned nil error, want an error")
	}
}
