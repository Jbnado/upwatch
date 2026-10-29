package notifier

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
)

// NewEmailTrusting monta o canal de e-mail confiando nas raízes dadas.
//
// Só existe nos testes: o servidor SMTP falso usa certificado próprio, e
// produção precisa continuar validando contra as raízes do sistema.
func NewEmailTrusting(raw json.RawMessage, roots *x509.CertPool) (Notifier, error) {
	return newEmail(raw, roots)
}

// NewTeamsWithClient monta o canal do Teams com o cliente HTTP dado.
//
// O canal só aceita https, e o servidor falso dos testes tem certificado
// próprio.
func NewTeamsWithClient(raw json.RawMessage, cliente *http.Client) (Notifier, error) {
	return newTeams(raw, cliente)
}
