package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// MaxBodyBytes limita quanto do corpo é lido para avaliar as condições.
//
// Um alvo que responda gigabytes não pode consumir a memória do monitor —
// e monitorar não deveria ser mais caro que ser monitorado.
const MaxBodyBytes = 1 << 20 // 1 MiB

// userAgent identifica o UpWatch para que o operador do alvo saiba de onde
// vem o tráfego ao investigar os próprios logs.
const userAgent = "UpWatch/0.1 (+https://github.com/bernardojoao/upwatch)"

// maxRedirects é o teto de saltos seguidos, para um laço de
// redirecionamento virar falha em vez de execução infinita.
const maxRedirects = 10

// HTTPConfig é a configuração específica de um monitor HTTP.
type HTTPConfig struct {
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`

	// ExpectStatus lista os códigos aceitos. Vazio aceita qualquer 2xx,
	// que cobre o caso comum sem exigir configuração.
	ExpectStatus []int `json:"expect_status,omitempty"`

	BodyContains    string `json:"body_contains,omitempty"`
	BodyNotContains string `json:"body_not_contains,omitempty"`
	BodyRegex       string `json:"body_regex,omitempty"`

	// FollowRedirects é ponteiro para distinguir "não informado" de
	// "explicitamente falso"; o padrão é seguir.
	FollowRedirects *bool `json:"follow_redirects,omitempty"`

	SkipTLSVerify bool `json:"skip_tls_verify,omitempty"`

	BasicAuthUser string `json:"basic_auth_user,omitempty"`
	BasicAuthPass string `json:"basic_auth_pass,omitempty"`
}

func (c HTTPConfig) method() string {
	if c.Method == "" {
		return http.MethodGet
	}
	return strings.ToUpper(c.Method)
}

func (c HTTPConfig) followRedirects() bool {
	return c.FollowRedirects == nil || *c.FollowRedirects
}

// accepts informa se o código de status satisfaz a configuração.
func (c HTTPConfig) accepts(code int) bool {
	if len(c.ExpectStatus) == 0 {
		return code >= 200 && code < 300
	}
	for _, want := range c.ExpectStatus {
		if want == code {
			return true
		}
	}
	return false
}

// HTTP verifica alvos por requisição HTTP(S).
type HTTP struct {
	// Dois transportes, um por política de TLS. O transporte guarda o pool
	// de conexões, então compartilhá-lo entre checks preserva keep-alive;
	// o http.Client em si é descartável e criado por requisição para
	// carregar timeout e política de redirecionamento próprios.
	secure   *http.Transport
	insecure *http.Transport
}

// NewHTTP cria o checker HTTP.
func NewHTTP() *HTTP {
	secure := http.DefaultTransport.(*http.Transport).Clone()
	insecure := http.DefaultTransport.(*http.Transport).Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opção explícita por monitor

	return &HTTP{secure: secure, insecure: insecure}
}

// Type identifica o tipo de monitor atendido.
func (h *HTTP) Type() domain.MonitorType { return domain.MonitorHTTP }

// ValidateConfig recusa configuração inválida no cadastro, para o erro não
// aparecer só na primeira execução do monitor.
func (h *HTTP) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parseHTTPConfig(raw)
	if err != nil {
		return err
	}

	if cfg.BodyRegex != "" {
		if _, err := regexp.Compile(cfg.BodyRegex); err != nil {
			return fmt.Errorf("checker: expressão regular inválida em body_regex: %w", err)
		}
	}
	if !isValidMethod(cfg.method()) {
		return fmt.Errorf("checker: método HTTP desconhecido %q", cfg.Method)
	}
	for _, code := range cfg.ExpectStatus {
		if code < 100 || code > 599 {
			return fmt.Errorf("checker: código de status fora da faixa válida: %d", code)
		}
	}
	return nil
}

// Check executa a requisição e avalia as condições configuradas.
//
// Nunca devolve erro: falha de rede é resultado Down com a causa, para o
// agendador registrar exatamente uma batida por execução.
func (h *HTTP) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseHTTPConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	var body io.Reader
	if cfg.Body != "" {
		body = strings.NewReader(cfg.Body)
	}

	req, err := http.NewRequestWithContext(ctx, cfg.method(), m.Target, body)
	if err != nil {
		return down("requisição inválida: %v", err)
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	if cfg.BasicAuthUser != "" || cfg.BasicAuthPass != "" {
		req.SetBasicAuth(cfg.BasicAuthUser, cfg.BasicAuthPass)
	}

	start := time.Now()
	resp, err := h.client(cfg).Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		res := down("%v", err)
		res.LatencyMS = latency
		return res
	}
	defer resp.Body.Close()

	res := Result{
		LatencyMS: latency,
		Meta:      map[string]string{"status_code": strconv.Itoa(resp.StatusCode)},
	}

	if !cfg.accepts(resp.StatusCode) {
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("status %d fora do esperado", resp.StatusCode)
		return res
	}

	// Lê no máximo MaxBodyBytes; conteúdo além do teto não é considerado
	// nas condições e nunca chega à memória.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("lendo o corpo da resposta: %v", err)
		return res
	}

	if msg := evaluateBody(cfg, string(payload)); msg != "" {
		res.Status = domain.StatusDown
		res.Message = msg
		return res
	}

	res.Status = domain.StatusUp
	return res
}

// client monta um cliente com o timeout e a política de redirecionamento
// do monitor, reaproveitando o transporte compartilhado.
func (h *HTTP) client(cfg HTTPConfig) *http.Client {
	transport := h.secure
	if cfg.SkipTLSVerify {
		transport = h.insecure
	}

	c := &http.Client{Transport: transport}
	if !cfg.followRedirects() {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		return c
	}
	c.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("mais de %d redirecionamentos seguidos", maxRedirects)
		}
		return nil
	}
	return c
}

// evaluateBody devolve a causa da reprovação, ou string vazia se o corpo
// satisfaz todas as condições.
func evaluateBody(cfg HTTPConfig, body string) string {
	if cfg.BodyContains != "" && !strings.Contains(body, cfg.BodyContains) {
		return fmt.Sprintf("corpo não contém %q", cfg.BodyContains)
	}
	if cfg.BodyNotContains != "" && strings.Contains(body, cfg.BodyNotContains) {
		return fmt.Sprintf("corpo contém o termo proibido %q", cfg.BodyNotContains)
	}
	if cfg.BodyRegex != "" {
		re, err := regexp.Compile(cfg.BodyRegex)
		if err != nil {
			return fmt.Sprintf("expressão regular inválida: %v", err)
		}
		if !re.MatchString(body) {
			return fmt.Sprintf("corpo não casa com %q", cfg.BodyRegex)
		}
	}
	return ""
}

func parseHTTPConfig(raw json.RawMessage) (HTTPConfig, error) {
	var cfg HTTPConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração HTTP inválida: %w", err)
	}
	return cfg, nil
}

var validMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	http.MethodOptions: true,
}

func isValidMethod(m string) bool { return validMethods[m] }

// down monta um resultado de falha com a causa formatada.
func down(format string, args ...any) Result {
	return Result{Status: domain.StatusDown, Message: fmt.Sprintf(format, args...)}
}
