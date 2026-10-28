// Package smtptest sobe um servidor SMTP falso, em memória, para testes.
//
// Existe pelo mesmo motivo que net/http/httptest: o canal de e-mail só
// prova que funciona conversando com um servidor de verdade, e depender
// de um servidor externo faria a suíte falhar por motivo alheio ao código.
// O servidor fala o suficiente do protocolo para exercitar o que importa
// ao canal — STARTTLS, TLS implícito, autenticação e recusa — e guarda o
// que recebeu para o teste inspecionar.
package smtptest

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Mode é como o servidor oferece criptografia.
type Mode int

const (
	// Plain não oferece TLS nenhum.
	Plain Mode = iota
	// StartTLS começa em claro e anuncia STARTTLS.
	StartTLS
	// ImplicitTLS exige TLS desde o primeiro byte, como na porta 465.
	ImplicitTLS
)

// Options configura o servidor falso.
type Options struct {
	Mode Mode
	// Username e Password, quando preenchidos, passam a ser exigidos.
	Username string
	Password string
	// AuthMechanisms é o que o servidor anuncia. Vazio vale "PLAIN LOGIN".
	AuthMechanisms string
}

// Message é uma mensagem aceita pelo servidor.
type Message struct {
	From string
	To   []string
	Data string
}

// Server é o servidor SMTP falso.
type Server struct {
	opts Options
	ln   net.Listener
	cert tls.Certificate
	// RootCAs confia no certificado do servidor, para o cliente do teste
	// poder validar o TLS como faria em produção.
	RootCAs *x509.CertPool

	mu       sync.Mutex
	messages []Message
	wg       sync.WaitGroup
}

// NewServer sobe o servidor em 127.0.0.1 numa porta livre.
func NewServer(t testing.TB, opts Options) *Server {
	t.Helper()

	// O certificado do httptest vale para 127.0.0.1 e é estável entre
	// execuções: gerar um próprio só repetiria o que ele já faz bem.
	ts := httptest.NewUnstartedServer(nil)
	ts.StartTLS()
	cert := ts.TLS.Certificates[0]
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	ts.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtptest: abrindo porta: %v", err)
	}
	if opts.Mode == ImplicitTLS {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	}
	if opts.AuthMechanisms == "" {
		opts.AuthMechanisms = "PLAIN LOGIN"
	}

	s := &Server{opts: opts, ln: ln, cert: cert, RootCAs: pool}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Host é o endereço de escuta, sempre 127.0.0.1.
func (s *Server) Host() string { return "127.0.0.1" }

// Port é a porta sorteada.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// Messages devolve as mensagens aceitas até agora.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Close encerra o servidor.
func (s *Server) Close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.session(conn)
		}()
	}
}

// session conduz uma conversa SMTP do começo ao fim.
func (s *Server) session(conn net.Conn) {
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(linhas ...string) {
		for _, l := range linhas {
			_, _ = w.WriteString(l + "\r\n")
		}
		_ = w.Flush()
	}

	emTLS := s.opts.Mode == ImplicitTLS
	autenticado := s.opts.Username == ""
	var atual Message

	reply("220 smtptest pronto")
	for {
		linha, err := r.ReadString('\n')
		if err != nil {
			return
		}
		linha = strings.TrimRight(linha, "\r\n")
		verbo, resto, _ := strings.Cut(linha, " ")

		switch strings.ToUpper(verbo) {
		case "EHLO", "HELO":
			ext := []string{"250-smtptest"}
			if s.opts.Mode == StartTLS && !emTLS {
				ext = append(ext, "250-STARTTLS")
			}
			if s.opts.Username != "" {
				ext = append(ext, "250-AUTH "+s.opts.AuthMechanisms)
			}
			ext = append(ext, "250 8BITMIME")
			reply(ext...)

		case "STARTTLS":
			if s.opts.Mode != StartTLS || emTLS {
				reply("502 STARTTLS indisponível")
				continue
			}
			reply("220 pode começar")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, emTLS = tc, true
			r, w = bufio.NewReader(conn), bufio.NewWriter(conn)

		case "AUTH":
			mecanismo, inicial, _ := strings.Cut(resto, " ")
			var user, pass string
			switch strings.ToUpper(mecanismo) {
			case "PLAIN":
				if inicial == "" {
					reply("334 ")
					inicial, _ = readLine(r)
				}
				dec, _ := base64.StdEncoding.DecodeString(inicial)
				partes := strings.SplitN(string(dec), "\x00", 3)
				if len(partes) == 3 {
					user, pass = partes[1], partes[2]
				}
			case "LOGIN":
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := readLine(r)
				reply("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := readLine(r)
				ud, _ := base64.StdEncoding.DecodeString(u)
				pd, _ := base64.StdEncoding.DecodeString(p)
				user, pass = string(ud), string(pd)
			default:
				reply("504 mecanismo não suportado")
				continue
			}
			if user == s.opts.Username && pass == s.opts.Password {
				autenticado = true
				reply("235 autenticado")
			} else {
				reply("535 5.7.8 Authentication credentials invalid")
			}

		case "MAIL":
			if !autenticado {
				reply("530 5.7.0 Authentication required")
				continue
			}
			atual = Message{From: endereco(resto)}
			reply("250 ok")

		case "RCPT":
			atual.To = append(atual.To, endereco(resto))
			reply("250 ok")

		case "DATA":
			reply("354 manda")
			var corpo strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				corpo.WriteString(strings.TrimPrefix(l, "."))
			}
			atual.Data = corpo.String()
			s.mu.Lock()
			s.messages = append(s.messages, atual)
			s.mu.Unlock()
			reply("250 aceita")

		case "RSET", "NOOP":
			reply("250 ok")

		case "QUIT":
			reply("221 até")
			return

		default:
			reply("500 comando desconhecido " + strconv.Quote(verbo))
		}
	}
}

func readLine(r *bufio.Reader) (string, error) {
	l, err := r.ReadString('\n')
	return strings.TrimRight(l, "\r\n"), err
}

// endereco extrai o que está entre < e > em "FROM:<x@y>".
func endereco(arg string) string {
	ini := strings.IndexByte(arg, '<')
	fim := strings.IndexByte(arg, '>')
	if ini < 0 || fim < ini {
		return ""
	}
	return arg[ini+1 : fim]
}
