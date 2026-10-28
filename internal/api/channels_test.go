package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Jbnado/upwatch/internal/notifier/smtptest"
)

// A senha do SMTP e o token do ntfy são credenciais de terceiros: a senha
// costuma ser a da conta de e-mail corporativa, e o token publica no tópico
// de quem recebe o alerta. Devolvê-los na leitura os exporia a qualquer
// observador com acesso à interface, e a qualquer token de leitura.
func TestChannelSecretsNeverComeBack(t *testing.T) {
	casos := []struct {
		tipo    string
		config  map[string]any
		segredo string
	}{
		{"email", map[string]any{
			"host": "smtp.exemplo.com", "username": "alertas",
			"password": "senha-do-smtp-secreta",
			"from":     "alertas@exemplo.com", "to": []string{"plantao@exemplo.com"},
		}, "senha-do-smtp-secreta"},
		{"ntfy", map[string]any{
			"server": "https://ntfy.exemplo.com", "topic": "infra",
			"token": "tk_token-do-ntfy-secreto",
		}, "tk_token-do-ntfy-secreto"},
	}

	for _, c := range casos {
		t.Run(c.tipo, func(t *testing.T) {
			s := newServer(t)
			s.setup(t)

			resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
				"name": "canal " + c.tipo, "type": c.tipo, "config": c.config,
			})
			criado := readBody(t, resp)
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("create returned %d, want 201: %s", resp.StatusCode, criado)
			}
			if strings.Contains(criado, c.segredo) {
				t.Errorf("the create response leaks the secret: %s", criado)
			}

			var id int64
			if _, err := fmt.Sscanf(criado[strings.Index(criado, `"id":`)+5:], "%d", &id); err != nil {
				t.Fatalf("no id in the create response: %s", criado)
			}

			for _, caminho := range []string{"/api/v1/channels", fmt.Sprintf("/api/v1/channels/%d", id)} {
				r := s.do(t, http.MethodGet, caminho, nil)
				corpo := readBody(t, r)
				if r.StatusCode != http.StatusOK {
					t.Fatalf("GET %s returned %d: %s", caminho, r.StatusCode, corpo)
				}
				if strings.Contains(corpo, c.segredo) {
					t.Errorf("GET %s leaks the secret: %s", caminho, corpo)
				}
			}
		})
	}
}

// O botão de teste é onde se descobre o certificado errado. A resposta
// precisa apontar o TLS por extenso, e não um "EOF" que mandaria procurar
// o problema na rede.
func TestTestingAnEmailChannelExplainsTheTLSFailure(t *testing.T) {
	s := newServer(t)
	s.setup(t)

	// Certificado autoassinado: o servidor da API não confia nele, como
	// não confiaria num relé interno com certificado próprio.
	smtp := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.ImplicitTLS})

	resp := s.do(t, http.MethodPost, "/api/v1/channels", map[string]any{
		"name": "e-mail do plantão", "type": "email",
		"config": map[string]any{
			"host": smtp.Host(), "port": smtp.Port(), "security": "tls",
			"from": "alertas@exemplo.com", "to": []string{"plantao@exemplo.com"},
		},
	})
	criado := readBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d, want 201: %s", resp.StatusCode, criado)
	}
	var id int64
	_, _ = fmt.Sscanf(criado[strings.Index(criado, `"id":`)+5:], "%d", &id)

	teste := s.do(t, http.MethodPost, fmt.Sprintf("/api/v1/channels/%d/test", id), nil)
	corpo := readBody(t, teste)

	if teste.StatusCode != http.StatusBadGateway {
		t.Fatalf("test returned %d, want 502: %s", teste.StatusCode, corpo)
	}
	if !strings.Contains(corpo, "TLS") || !strings.Contains(corpo, "certificado") {
		t.Errorf("the test response does not explain the TLS failure: %s", corpo)
	}
	if len(smtp.Messages()) != 0 {
		t.Error("a message went through an untrusted connection")
	}
}
