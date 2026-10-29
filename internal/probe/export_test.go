package probe

import (
	"crypto/tls"
	"crypto/x509"
	"time"
)

// NewSenderForTest confia na CA do servidor de teste e encurta o limite
// de cada entrega, para o teste não esperar os 15s de produção.
func NewSenderForTest(version string, roots *x509.CertPool, timeout time.Duration) *Sender {
	return newSender(version, &tls.Config{RootCAs: roots}, timeout)
}
