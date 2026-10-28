package notifier

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Modos de criptografia do SMTP.
const (
	smtpStartTLS = "starttls"
	smtpTLS      = "tls"
	smtpNone     = "none"
)

// portaPadrao é a porta que cada modo usa quando ninguém diz outra.
var portaPadrao = map[string]int{smtpStartTLS: 587, smtpTLS: 465, smtpNone: 25}

// emailConfig é a configuração do canal de e-mail.
type emailConfig struct {
	Host string `json:"host"`
	// Port vazia segue o modo: 587 para STARTTLS, 465 para TLS, 25 sem
	// criptografia.
	Port int `json:"port,omitempty"`
	// Security é starttls (padrão), tls ou none.
	Security string `json:"security,omitempty"`
	Username string `json:"username,omitempty"`
	// Password é segredo. Como o resto da configuração, nunca sai pela
	// API.
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

type email struct {
	cfg  emailConfig
	from *mail.Address
	to   []*mail.Address
	// roots substitui as raízes do sistema. Nulo em produção; existe para
	// o teste conversar com um servidor de certificado próprio.
	roots *x509.CertPool
}

// NewEmail cria o canal de e-mail por SMTP.
//
// É o canal mais universal: onde chat pessoal não é canal oficial, o
// plantão é avisado por e-mail, e é para e-mail que listas de distribuição
// e sistemas de chamado escutam.
func NewEmail(raw json.RawMessage) (Notifier, error) { return newEmail(raw, nil) }

func newEmail(raw json.RawMessage, roots *x509.CertPool) (Notifier, error) {
	var cfg emailConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("notifier: configuração de e-mail inválida: %w", err)
	}

	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return nil, fmt.Errorf("notifier: o canal de e-mail precisa do servidor SMTP (host)")
	}

	cfg.Security = strings.ToLower(strings.TrimSpace(cfg.Security))
	if cfg.Security == "" {
		cfg.Security = smtpStartTLS
	}
	padrao, ok := portaPadrao[cfg.Security]
	if !ok {
		return nil, fmt.Errorf("notifier: security %q desconhecido; use starttls, tls ou none", cfg.Security)
	}
	if cfg.Port == 0 {
		cfg.Port = padrao
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("notifier: porta SMTP %d fora da faixa", cfg.Port)
	}

	// Credencial em texto puro pela rede é exatamente o que STARTTLS e TLS
	// existem para impedir. Recusar no cadastro é melhor que descobrir
	// depois que a senha do e-mail corporativo andou aberta.
	if cfg.Security == smtpNone && (cfg.Username != "" || cfg.Password != "") {
		return nil, fmt.Errorf("notifier: usuário e senha exigem starttls ou tls; " +
			"sem criptografia, a senha atravessaria a rede aberta")
	}

	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("notifier: remetente (from) inválido: %q", cfg.From)
	}

	if len(cfg.To) == 0 {
		return nil, fmt.Errorf("notifier: o canal de e-mail precisa de ao menos um destinatário (to)")
	}
	to := make([]*mail.Address, 0, len(cfg.To))
	for _, bruto := range cfg.To {
		addr, err := mail.ParseAddress(bruto)
		if err != nil {
			return nil, fmt.Errorf("notifier: destinatário inválido: %q", bruto)
		}
		to = append(to, addr)
	}

	return &email{cfg: cfg, from: from, to: to, roots: roots}, nil
}

func (e *email) Send(ctx context.Context, n Notification) error {
	msg, err := e.compose(n, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	c, err := e.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if err := c.Mail(e.from.Address); err != nil {
		return fmt.Errorf("notifier: o servidor SMTP recusou o remetente %s: %w", e.from.Address, err)
	}
	for _, rcpt := range e.to {
		if err := c.Rcpt(rcpt.Address); err != nil {
			return fmt.Errorf("notifier: o servidor SMTP recusou o destinatário %s: %w", rcpt.Address, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("notifier: o servidor SMTP recusou a mensagem: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("notifier: enviando a mensagem: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notifier: o servidor SMTP recusou a mensagem: %w", err)
	}

	// O QUIT é cortesia: a mensagem já foi aceita no fim do DATA, e uma
	// falha aqui não pode virar retentativa e e-mail duplicado.
	_ = c.Quit()
	return nil
}

// connect abre a conversa SMTP já cifrada e autenticada.
func (e *email) connect(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(e.cfg.Host, strconv.Itoa(e.cfg.Port))

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("notifier: não foi possível conectar ao servidor SMTP %s: %w", addr, err)
	}

	// O prazo vale para a conversa inteira, não só para a conexão: um
	// servidor que aceita e depois fica mudo seguraria a fila do mesmo
	// jeito.
	if prazo, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(prazo)
	}

	tlsCfg := &tls.Config{ServerName: e.cfg.Host, RootCAs: e.roots, MinVersion: tls.VersionTLS12}

	if e.cfg.Security == smtpTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, explainTLS(addr, err)
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("notifier: %s não respondeu como servidor SMTP: %w", addr, err)
	}

	if e.cfg.Security == smtpStartTLS {
		// Sem STARTTLS anunciado, seguir em claro seria rebaixar a
		// segurança em silêncio — e a senha iria junto.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			_ = c.Close()
			return nil, fmt.Errorf("notifier: o servidor SMTP %s não oferece STARTTLS; "+
				"se ele usa TLS implícito (porta 465), escolha tls", addr)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			_ = c.Close()
			return nil, explainTLS(addr, err)
		}
	}

	if e.cfg.Username != "" {
		if err := c.Auth(e.auth(c)); err != nil {
			_ = c.Close()
			return nil, explainAuth(err)
		}
	}
	return c, nil
}

// auth escolhe o mecanismo que o servidor aceita.
//
// PLAIN é o padrão; LOGIN é o que Microsoft 365 e muitos relés
// corporativos anunciam sozinho, e sem ele o canal não funcionaria
// justamente onde e-mail é o canal oficial.
func (e *email) auth(c *smtp.Client) smtp.Auth {
	_, mecanismos := c.Extension("AUTH")

	temPlain, temLogin := false, false
	for _, m := range strings.Fields(strings.ToUpper(mecanismos)) {
		temPlain = temPlain || m == "PLAIN"
		temLogin = temLogin || m == "LOGIN"
	}
	if temLogin && !temPlain {
		return &loginAuth{user: e.cfg.Username, pass: e.cfg.Password}
	}
	return smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.Host)
}

// loginAuth implementa AUTH LOGIN, que net/smtp não traz.
type loginAuth struct{ user, pass string }

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	// Mesmo cuidado do PlainAuth da biblioteca padrão: LOGIN manda a senha
	// só em base64, então nunca fora de TLS.
	if !server.TLS {
		return "", nil, errors.New("AUTH LOGIN recusado sem TLS")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	pergunta := strings.ToLower(string(fromServer))
	switch {
	case strings.Contains(pergunta, "user"):
		return []byte(a.user), nil
	case strings.Contains(pergunta, "pass"):
		return []byte(a.pass), nil
	default:
		return nil, fmt.Errorf("pergunta inesperada no AUTH LOGIN: %q", fromServer)
	}
}

// explainTLS traduz a falha de TLS para quem está diante do botão de teste.
func explainTLS(addr string, err error) error {
	var verificacao *tls.CertificateVerificationError
	var autoridade x509.UnknownAuthorityError
	var nome x509.HostnameError
	if errors.As(err, &verificacao) || errors.As(err, &autoridade) || errors.As(err, &nome) {
		return fmt.Errorf("notifier: falha no TLS com %s: o certificado do servidor não é confiável (%v)", addr, err)
	}
	return fmt.Errorf("notifier: falha no TLS com %s: %v; confira se a porta espera TLS implícito "+
		"(tls, 465) ou STARTTLS (starttls, 587)", addr, err)
}

// explainAuth traduz a recusa de credencial.
//
// "535" sozinho não diz nada a quem não decorou os códigos do SMTP. A
// mensagem do servidor vai junto, mas nunca a senha.
func explainAuth(err error) error {
	var proto *textproto.Error
	if errors.As(err, &proto) && proto.Code == 535 {
		return fmt.Errorf("notifier: falha na autenticação SMTP: o servidor recusou o usuário e senha (%d %s)",
			proto.Code, proto.Msg)
	}
	return fmt.Errorf("notifier: falha na autenticação SMTP: %v", err)
}

// ---------- mensagem ----------

// emailSubject é o assunto, curto e estável.
//
// Sempre "[UpWatch] <monitor>: <estado>": é o que o cliente de e-mail
// agrupa e o que regra de caixa de entrada ou sistema de chamado
// reconhece. Caractere de controle no nome vira espaço, para uma quebra
// de linha não abrir um cabeçalho novo.
func emailSubject(n Notification) string {
	nome := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, n.Monitor.Name)
	return fmt.Sprintf("[UpWatch] %s: %s", nome, statusLabel(n.Event.To))
}

// emailCampo é uma linha do resumo do aviso.
type emailCampo struct{ Rotulo, Valor string }

// emailCampos lista o que o corpo mostra, na ordem de leitura.
//
// Uma lista só para as duas partes: decidir em dois lugares o que entra
// no texto e o que entra no HTML faria as duas divergirem.
func emailCampos(n Notification) []emailCampo {
	var campos []emailCampo
	if n.Monitor.Target != "" {
		campos = append(campos, emailCampo{"Alvo", n.Monitor.Target})
	}
	if n.Message != "" {
		campos = append(campos, emailCampo{"Causa", n.Message})
	}
	campos = append(campos, emailCampo{"Horário", n.Event.At.Format("02/01/2006 15:04:05 MST")})
	if n.Event.Resolves() && n.Event.Duration > 0 {
		campos = append(campos, emailCampo{"Fora do ar por", humanDuration(n.Event.Duration)})
	}
	return campos
}

// emailHTML escapa nome e causa: os dois vêm de fora — de quem cadastrou
// e do alvo monitorado — e um alvo hostil não pode pôr script ou link
// falso no alerta, que é o que se lê com pressa e se confia.
var emailHTML = template.Must(template.New("email").Parse(`<!doctype html>
<html lang="pt-BR"><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#1a1a1a">
<p style="font-size:16px;margin:0 0 16px"><strong>{{.Frase}}</strong></p>
<table style="border-collapse:collapse;font-size:14px">
{{range .Campos}}<tr><td style="padding:2px 16px 2px 0;color:#666">{{.Rotulo}}</td><td style="padding:2px 0">{{.Valor}}</td></tr>
{{end}}</table>
<p style="margin:24px 0 0;font-size:12px;color:#888">Enviado pelo UpWatch.</p>
</body></html>
`))

// compose monta a mensagem completa, cabeçalhos e corpo multipart.
func (e *email) compose(n Notification, agora time.Time) ([]byte, error) {
	frase := Render(n)
	campos := emailCampos(n)

	var texto bytes.Buffer
	texto.WriteString(frase + "\n\n")
	for _, c := range campos {
		fmt.Fprintf(&texto, "%s: %s\n", c.Rotulo, c.Valor)
	}
	texto.WriteString("\n-- \nEnviado pelo UpWatch.\n")

	var html bytes.Buffer
	if err := emailHTML.Execute(&html, map[string]any{"Frase": frase, "Campos": campos}); err != nil {
		return nil, fmt.Errorf("notifier: montando o HTML do e-mail: %w", err)
	}

	var corpo bytes.Buffer
	mw := multipart.NewWriter(&corpo)
	for _, parte := range []struct {
		tipo string
		dado []byte
	}{{"text/plain", texto.Bytes()}, {"text/html", html.Bytes()}} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {parte.tipo + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write(parte.dado); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	destinos := make([]string, 0, len(e.to))
	for _, a := range e.to {
		destinos = append(destinos, a.String())
	}

	var msg bytes.Buffer
	cabecalho := func(nome, valor string) { fmt.Fprintf(&msg, "%s: %s\r\n", nome, valor) }
	cabecalho("From", e.from.String())
	cabecalho("To", strings.Join(destinos, ", "))
	cabecalho("Subject", mime.QEncoding.Encode("utf-8", emailSubject(n)))
	cabecalho("Date", agora.Format(time.RFC1123Z))
	cabecalho("Message-ID", messageID(e.from.Address))
	// Pede a resposta automática e ao "estou de férias" que não respondam
	// ao alerta (RFC 3834).
	cabecalho("Auto-Submitted", "auto-generated")
	cabecalho("MIME-Version", "1.0")
	cabecalho("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
	msg.WriteString("\r\n")
	msg.Write(corpo.Bytes())
	return msg.Bytes(), nil
}

// messageID gera um identificador único no domínio do remetente.
//
// Sem ele, vários servidores pontuam a mensagem como spam — e alerta na
// caixa de spam é alerta que não chegou.
func messageID(from string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	dominio := "upwatch.local"
	if _, d, ok := strings.Cut(from, "@"); ok && d != "" {
		dominio = d
	}
	return "<" + hex.EncodeToString(b) + "@" + dominio + ">"
}
