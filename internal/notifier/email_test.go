package notifier_test

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/notifier"
	"github.com/Jbnado/upwatch/internal/notifier/smtptest"
)

// emailConfig monta a configuração apontando para o servidor falso.
func emailConfig(srv *smtptest.Server, security string, extra string) json.RawMessage {
	cfg := fmt.Sprintf(`{"host":%q,"port":%d,"security":%q,`+
		`"from":"UpWatch <alertas@exemplo.com>","to":["plantao@exemplo.com","infra@exemplo.com"]%s}`,
		srv.Host(), srv.Port(), security, extra)
	return json.RawMessage(cfg)
}

func newEmail(t *testing.T, raw json.RawMessage, roots *x509.CertPool) notifier.Notifier {
	t.Helper()
	n, err := notifier.NewEmailTrusting(raw, roots)
	if err != nil {
		t.Fatalf("NewEmail returned unexpected error: %v", err)
	}
	return n
}

// partes separa o texto e o HTML de uma mensagem multipart.
func partes(t *testing.T, data string) (*mail.Message, string, string) {
	t.Helper()

	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("the message is not valid RFC 5322: %v", err)
	}
	tipo, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || tipo != "multipart/alternative" {
		t.Fatalf("Content-Type = %q, want multipart/alternative", msg.Header.Get("Content-Type"))
	}

	var texto, html string
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading a MIME part: %v", err)
		}
		corpo, _ := io.ReadAll(p)
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		switch ct {
		case "text/plain":
			texto = string(corpo)
		case "text/html":
			html = string(corpo)
		}
	}
	return msg, texto, html
}

func assunto(t *testing.T, msg *mail.Message) string {
	t.Helper()
	s, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decoding Subject: %v", err)
	}
	return s
}

// A queda precisa chegar com o assunto previsível: é por ele que o cliente
// de e-mail agrupa e que regra de caixa de entrada ou sistema de chamado
// reconhece o alerta.
func TestEmailDeliversOutageOverSTARTTLS(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.StartTLS, Username: "upwatch", Password: "s3nha",
	})
	e := newEmail(t, emailConfig(srv, "starttls", `,"username":"upwatch","password":"s3nha"`), srv.RootCAs)

	if err := send(t, e, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(msgs))
	}
	if msgs[0].From != "alertas@exemplo.com" {
		t.Errorf("envelope sender = %q, want the bare address", msgs[0].From)
	}
	if strings.Join(msgs[0].To, ",") != "plantao@exemplo.com,infra@exemplo.com" {
		t.Errorf("envelope recipients = %v, want both configured addresses", msgs[0].To)
	}

	msg, texto, html := partes(t, msgs[0].Data)
	if got := assunto(t, msg); got != "[UpWatch] api de produção: fora do ar" {
		t.Errorf("Subject = %q, want the stable outage subject", got)
	}
	// A causa decide se alguém levanta da cadeira; o alvo diz onde olhar.
	for _, esperado := range []string{"502", "https://api.exemplo.com/health"} {
		if !strings.Contains(texto, esperado) {
			t.Errorf("the text part omits %q:\n%s", esperado, texto)
		}
		if !strings.Contains(html, esperado) {
			t.Errorf("the HTML part omits %q:\n%s", esperado, html)
		}
	}
}

// A volta precisa dizer quanto tempo durou a queda, que é a primeira
// pergunta de quem foi acordado por ela. O TLS implícito da porta 465 é o
// que muitos provedores ainda exigem.
func TestEmailDeliversRecoveryOverImplicitTLS(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.ImplicitTLS, Username: "upwatch", Password: "s3nha",
	})
	e := newEmail(t, emailConfig(srv, "tls", `,"username":"upwatch","password":"s3nha"`), srv.RootCAs)

	if err := send(t, e, recovery(42*time.Minute)); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(msgs))
	}
	msg, texto, _ := partes(t, msgs[0].Data)
	if got := assunto(t, msg); got != "[UpWatch] api de produção: no ar" {
		t.Errorf("Subject = %q, want the stable recovery subject", got)
	}
	if !strings.Contains(texto, "42 min") {
		t.Errorf("the recovery text omits the outage duration:\n%s", texto)
	}
}

// Servidor do Microsoft 365 e vários relés corporativos só anunciam AUTH
// LOGIN. Sem ele, o canal mais procurado em empresa não funcionaria
// justamente onde ele é mais procurado.
func TestEmailAuthenticatesWithLOGINWhenPLAINIsAbsent(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.StartTLS, Username: "upwatch", Password: "s3nha", AuthMechanisms: "LOGIN",
	})
	e := newEmail(t, emailConfig(srv, "starttls", `,"username":"upwatch","password":"s3nha"`), srv.RootCAs)

	if err := send(t, e, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	if len(srv.Messages()) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(srv.Messages()))
	}
}

// O nome do monitor e a causa vêm de quem cadastrou e do alvo
// monitorado. Interpolados crus no HTML, um alvo hostil poria script ou
// link falso no e-mail de alerta, que é justamente o que se lê com pressa
// e se confia.
func TestEmailEscapesHTMLInNameAndCause(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.StartTLS})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)

	n := outage()
	n.Monitor.Name = `<script>alert("nome")</script>`
	n.Message = `<img src=x onerror="alert('causa')">`
	if err := send(t, e, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	_, _, html := partes(t, srv.Messages()[0].Data)
	for _, cru := range []string{"<script>", "<img"} {
		if strings.Contains(html, cru) {
			t.Errorf("the HTML part carries raw %q, want it escaped:\n%s", cru, html)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("the HTML part dropped the name instead of escaping it:\n%s", html)
	}
}

// Quebra de linha no nome do monitor, interpolada no assunto, viraria um
// cabeçalho novo — um Bcc escondido mandaria o alerta a quem não deveria.
func TestEmailSubjectCannotInjectHeaders(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.StartTLS})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)

	n := outage()
	n.Monitor.Name = "api\r\nBcc: intruso@exemplo.com"
	if err := send(t, e, n); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}

	msg, _, _ := partes(t, srv.Messages()[0].Data)
	if bcc := msg.Header.Get("Bcc"); bcc != "" {
		t.Errorf("the monitor name injected a Bcc header: %q", bcc)
	}
	if got := srv.Messages()[0].To; len(got) != 2 {
		t.Errorf("envelope recipients = %v, want only the configured ones", got)
	}
}

// O botão de teste é onde se descobre a senha errada. "535" sozinho não
// diz nada a quem não decorou os códigos do SMTP.
func TestEmailExplainsAuthenticationFailure(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.StartTLS, Username: "upwatch", Password: "certa",
	})
	e := newEmail(t, emailConfig(srv, "starttls", `,"username":"upwatch","password":"errada"`), srv.RootCAs)

	err := send(t, e, outage())
	if err == nil {
		t.Fatal("Send with a wrong password returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "autenticação") || !strings.Contains(err.Error(), "usuário e senha") {
		t.Errorf("error = %q, want it to say the credentials were refused", err)
	}
	if strings.Contains(err.Error(), "errada") {
		t.Errorf("error = %q leaks the password", err)
	}
}

// Certificado que não confere é o outro erro clássico de configuração. A
// mensagem precisa apontar o TLS, não um "EOF" que manda procurar na rede.
func TestEmailExplainsTLSFailure(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.ImplicitTLS})
	// Sem confiar no certificado do servidor falso, como seria um
	// certificado autoassinado em produção.
	e := newEmail(t, emailConfig(srv, "tls", ""), nil)

	err := send(t, e, outage())
	if err == nil {
		t.Fatal("Send against an untrusted certificate returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "TLS") || !strings.Contains(err.Error(), "certificado") {
		t.Errorf("error = %q, want it to point at the TLS certificate", err)
	}
}

// Pedir STARTTLS e o servidor não oferecer não pode cair para texto puro
// em silêncio: a senha atravessaria a rede aberta sem ninguém saber.
func TestEmailRefusesToDowngradeWhenSTARTTLSIsMissing(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.Plain})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)

	err := send(t, e, outage())
	if err == nil {
		t.Fatal("Send without STARTTLS on offer returned nil error, want an error")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("error = %q, want it to name the missing STARTTLS", err)
	}
	if len(srv.Messages()) != 0 {
		t.Error("the message went out in clear text")
	}
}

// Relé local sem criptografia é legítimo — o postfix da própria máquina
// é o caso comum. Sem credencial, não há segredo a proteger.
func TestEmailSendsInClearToALocalRelayWithoutCredentials(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.Plain})
	e := newEmail(t, emailConfig(srv, "none", ""), nil)

	if err := send(t, e, outage()); err != nil {
		t.Fatalf("Send returned unexpected error: %v", err)
	}
	if len(srv.Messages()) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(srv.Messages()))
	}
}

// Erro de configuração aparece no cadastro, e não durante o incidente,
// que é quando a mensagem não pode falhar.
func TestEmailRejectsInvalidConfigUpFront(t *testing.T) {
	casos := map[string]string{
		"sem servidor":       `{"from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		"sem remetente":      `{"host":"smtp.exemplo.com","to":["b@exemplo.com"]}`,
		"remetente inválido": `{"host":"smtp.exemplo.com","from":"não é endereço","to":["b@exemplo.com"]}`,
		"sem destinatário":   `{"host":"smtp.exemplo.com","from":"a@exemplo.com","to":[]}`,
		"destino inválido":   `{"host":"smtp.exemplo.com","from":"a@exemplo.com","to":["b@"]}`,
		"porta fora da faixa": `{"host":"smtp.exemplo.com","port":70000,` +
			`"from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		"segurança desconhecida": `{"host":"smtp.exemplo.com","security":"talvez",` +
			`"from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		// Senha em texto puro pela rede é o que STARTTLS e TLS existem
		// para impedir.
		"senha sem criptografia": `{"host":"smtp.exemplo.com","security":"none","username":"u",` +
			`"password":"p","from":"a@exemplo.com","to":["b@exemplo.com"]}`,
		"json malformado": `{nope`,
	}

	for nome, cfg := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := notifier.NewEmail(json.RawMessage(cfg)); err == nil {
				t.Fatalf("NewEmail(%s) returned nil error, want an error", cfg)
			}
		})
	}
}

// Um endereço recusado — a pessoa saiu da empresa, a lista foi apagada —
// não pode calar o alerta para o resto do plantão. Abortar no primeiro
// RCPT recusado fazia exatamente isso.
func TestEmailDeliversToAcceptedRecipientsWhenOneIsRefused(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.StartTLS, RejectRecipients: []string{"plantao@exemplo.com"},
	})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)

	if err := send(t, e, outage()); err != nil {
		t.Fatalf("Send returned %v, want the accepted recipient to get the alert", err)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("the server received %d messages, want 1", len(msgs))
	}
	if got := strings.Join(msgs[0].To, ","); got != "infra@exemplo.com" {
		t.Errorf("envelope recipients = %q, want only the accepted one", got)
	}
}

// Todos recusados é erro, e permanente: o servidor disse com 5xx que a
// caixa não existe, e repetir quatro vezes só suja o log e atrasa a fila.
func TestEmailAllRecipientsRefusedIsPermanent(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode:             smtptest.StartTLS,
		RejectRecipients: []string{"plantao@exemplo.com", "infra@exemplo.com"},
	})
	e := newEmail(t, emailConfig(srv, "starttls", ""), srv.RootCAs)

	err := send(t, e, outage())
	if err == nil {
		t.Fatal("Send with every recipient refused returned nil error, want an error")
	}
	if !notifier.IsPermanent(err) {
		t.Errorf("IsPermanent(%v) = false, want a 550 on every recipient to be permanent", err)
	}
	if len(srv.Messages()) != 0 {
		t.Error("a message was accepted with no recipient")
	}
}

// Senha errada não melhora com a espera. Repetir só gasta tentativas — e
// em servidor com bloqueio por tentativas, trava a conta.
func TestEmailAuthenticationFailureIsPermanent(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{
		Mode: smtptest.StartTLS, Username: "upwatch", Password: "certa",
	})
	e := newEmail(t, emailConfig(srv, "starttls", `,"username":"upwatch","password":"errada"`), srv.RootCAs)

	if err := send(t, e, outage()); !notifier.IsPermanent(err) {
		t.Errorf("IsPermanent(%v) = false, want a 535 to be permanent", err)
	}
}

// Servidor fora do ar é o caso clássico de falha momentânea: tratá-lo
// como permanente perderia o aviso por causa de um reinício do relé.
func TestEmailConnectionFailureIsTransient(t *testing.T) {
	srv := smtptest.NewServer(t, smtptest.Options{Mode: smtptest.Plain})
	cfg := emailConfig(srv, "none", "")
	srv.Close()
	e := newEmail(t, cfg, nil)

	err := send(t, e, outage())
	if err == nil {
		t.Fatal("Send to a closed server returned nil error, want an error")
	}
	if notifier.IsPermanent(err) {
		t.Errorf("IsPermanent(%v) = true, want a refused connection to be retried", err)
	}
}
