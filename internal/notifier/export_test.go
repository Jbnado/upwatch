package notifier

import (
	"crypto/x509"
	"encoding/json"
)

// NewEmailTrusting monta o canal de e-mail confiando nas raízes dadas.
//
// Só existe nos testes: o servidor SMTP falso usa certificado próprio, e
// produção precisa continuar validando contra as raízes do sistema.
func NewEmailTrusting(raw json.RawMessage, roots *x509.CertPool) (Notifier, error) {
	return newEmail(raw, roots)
}
