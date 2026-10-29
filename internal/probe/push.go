package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// Signal é tudo o que a sonda manda ao central sobre uma verificação.
//
// Três campos e nada mais: nenhum corpo de resposta, cabeçalho, endereço
// interno ou credencial sai da rede onde a sonda roda.
type Signal struct {
	Status    string `json:"status"`
	Msg       string `json:"msg,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// SignalFrom converte o resultado do checker no sinal do push.
//
// A causa passa pela mesma limpeza do central (caracteres de controle,
// formatação invisível, corte em 200 caracteres) antes de sair, para o
// que o dry-run mostra ser exatamente o que o central registra.
func SignalFrom(r checker.Result) Signal {
	status, msg := "down", r.Message
	switch r.Status {
	case domain.StatusUp:
		status = "up"
	case domain.StatusDegraded:
		status = "degraded"
	case domain.StatusUnknown:
		// O central recusa "unknown". Calar faria o monitor cair por
		// silêncio, com a causa de "sonda sumiu"; down diz o que houve.
		msg = "sem medição: " + r.Message
	}
	return Signal{
		Status:    status,
		Msg:       domain.CleanPushMessage(msg),
		LatencyMS: min(max(r.LatencyMS, 0), domain.MaxPushLatencyMS),
	}
}

// Body é o corpo exato da requisição de push.
//
// Sem o escape de HTML do encoding/json: "esperado >= 1" sairia como
// "esperado >= 1", que o central lê igual, mas que o dry-run
// mostraria ilegível justamente para quem confere o que sai da rede.
func (s Signal) Body() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // três campos simples: não há como falhar
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// SendError é uma entrega que não deu certo.
//
// A mensagem nunca traz o endereço: o erro do cliente HTTP do Go repete a
// URL inteira, com o token, e o erro vai para o log e para o status.
type SendError struct {
	HTTPStatus int
	// Retryable diz se outra tentativa pode dar certo. Token errado não
	// melhora com insistência; central fora do ar, sim.
	Retryable bool
	// RetryAfter é a espera pedida pelo central num 429 ou 503.
	RetryAfter time.Duration
	Msg        string
}

func (e *SendError) Error() string { return e.Msg }

// Sender entrega o sinal ao central, uma tentativa por chamada.
type Sender struct {
	agent     string
	newClient func() *http.Client
	client    atomic.Pointer[http.Client]
}

// sendTimeout limita uma entrega. O sinal tem poucas dezenas de bytes; o
// que passa disso é rede ruim, e a tentativa seguinte resolve melhor do
// que esperar.
const sendTimeout = 15 * time.Second

// O ping vai depois de 30s sem nada chegar pela conexão e, sem resposta
// em 10s, ela é descartada: menos que o intervalo de 60s das verificações.
const (
	http2PingAfter   = 30 * time.Second
	http2PingTimeout = 10 * time.Second
)

// NewSender cria o cliente de push.
//
// Respeita HTTPS_PROXY, que é como muita VM corporativa sai para a
// internet, e não segue redirecionamento: seguir mandaria o token a outro
// host, e o POST viraria GET sem corpo, que o central lê como "up".
func NewSender(version string) *Sender {
	return newSender(version, nil, sendTimeout)
}

func newSender(version string, tlsConfig *tls.Config, timeout time.Duration) *Sender {
	s := &Sender{
		agent: "upwatch-probe/" + version,
		newClient: func() *http.Client {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			if tlsConfig != nil {
				transport.TLSClientConfig = tlsConfig
			}
			// Uma conexão HTTP/2 que a NAT ou o firewall esquece no caminho
			// não fecha nem dá erro: sem o ping, o pool a reusa e todo
			// envio estoura o tempo, com a rede de pé.
			transport.HTTP2 = &http.HTTP2Config{
				SendPingTimeout: http2PingAfter,
				PingTimeout:     http2PingTimeout,
			}
			return &http.Client{
				Transport: transport,
				Timeout:   timeout,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
		},
	}
	s.client.Store(s.newClient())
	return s
}

// Send faz uma tentativa de entrega.
func (s *Sender) Send(ctx context.Context, pushURL string, sig Signal) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushURL, bytes.NewReader(sig.Body()))
	if err != nil {
		return &SendError{Msg: "endereço de push inválido"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.agent)

	resp, err := s.client.Load().Do(req)
	if err != nil {
		// A conexão que estourou o tempo pode estar morta sem saber, e o
		// HTTP/2 segue contando a requisição cancelada nela, então fechar
		// as ociosas não a solta. Um pool novo garante que a próxima
		// tentativa abra outra conexão.
		s.client.Swap(s.newClient()).CloseIdleConnections()
		return &SendError{Retryable: true, Msg: "falha de rede: " + withoutURL(err)}
	}
	defer resp.Body.Close()
	// A resposta não interessa, mas lê-la (até um limite) devolve a
	// conexão ao pool em vez de abrir outra a cada minuto.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return responseError(resp)
}

func responseError(resp *http.Response) *SendError {
	e := &SendError{HTTPStatus: resp.StatusCode}
	switch code := resp.StatusCode; {
	case code == http.StatusUnauthorized:
		e.Msg = "o central recusou o token (401): confira o endereço de push e se o monitor existe"
	case code == http.StatusNotFound:
		e.Msg = "endereço de push não encontrado (404): confira o endereço do central"
	case code == http.StatusTooManyRequests:
		e.Retryable = true
		e.Msg = "o central pediu para esperar (429)"
	case code >= 300 && code < 400:
		e.Msg = fmt.Sprintf("o central respondeu com redirecionamento (%d); use o endereço final, em https", code)
	case code >= 500:
		e.Retryable = true
		e.Msg = fmt.Sprintf("o central respondeu %d", code)
	default:
		e.Msg = fmt.Sprintf("o central recusou o sinal (%d)", code)
	}
	if e.Retryable {
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	}
	return e
}

// maxRetryAfter limita a espera pedida pelo central. Um cabeçalho com
// horas pararia o sinal de vida e derrubaria o monitor da própria sonda.
const maxRetryAfter = 5 * time.Minute

func retryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, maxRetryAfter)
}

// withoutURL devolve o erro sem a URL que o cliente HTTP põe nele.
func withoutURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if errors.Is(ue.Err, context.DeadlineExceeded) || ue.Timeout() {
			return "tempo esgotado"
		}
		return ue.Err.Error()
	}
	return err.Error()
}
