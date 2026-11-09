package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// MaxBodyBytes limita quanto do corpo é lido para avaliar as condições.
//
// Um alvo que responda gigabytes não pode consumir a memória do monitor —
// e monitorar não deveria ser mais caro que ser monitorado.
const MaxBodyBytes = 1 << 20 // 1 MiB

// userAgent identifica o UpWatch para que o operador do alvo saiba de onde
// vem o tráfego ao investigar os próprios logs.
const userAgent = "UpWatch/0.1 (+https://github.com/Jbnado/upwatch)"

// maxRedirects é o teto de saltos seguidos, para um laço de
// redirecionamento virar falha em vez de execução infinita.
const maxRedirects = 10

// MaxJSONAssertions limita quantas condições um monitor HTTP pode ter.
//
// Sem teto, o custo por verificação é escolhido por quem cadastra e pago
// pela instalação para sempre: cada asserção percorre o JSON já
// interpretado a cada batida, e a configuração inteira viaja de volta em
// toda listagem de monitores. O número é alto de propósito — o uso real é
// de duas a cinco condições —, para recusar só o que não é uso.
//
// Conferido no cadastro, e não na verificação: monitor que já exista com
// mais que isso continua funcionando, em vez de cair de uma vez numa
// atualização por um limite que não existia quando ele foi criado.
const MaxJSONAssertions = 50

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

	// JSONAssertions são condições sobre valores do corpo JSON. Todas
	// precisam valer; o corpo só é interpretado como JSON quando há alguma.
	JSONAssertions []JSONAssertion `json:"json_assertions,omitempty"`

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

	// Os mesmos dois para monitores com RestrictInternal. Pool à parte:
	// uma conexão aberta sem a conferência não pode ser reaproveitada por
	// um monitor que exige a conferência.
	restrictedSecure   *http.Transport
	restrictedInsecure *http.Transport

	// compiled guarda, por monitor, o que a configuração dele exige
	// compilar: as asserções JSON e a expressão regular do corpo. Junto
	// vai a configuração de onde vieram — editar o monitor troca a
	// configuração e invalida a entrada.
	mu       sync.Mutex
	compiled map[int64]compiledConfig
}

// compiledConfig é a parte da configuração que custa interpretar.
type compiledConfig struct {
	config     string
	assertions []compiledAssertion
	// bodyRegex é nulo quando o monitor não tem body_regex, que é o que
	// diz ao Check para não avaliar a condição.
	bodyRegex *regexp.Regexp
}

// NewHTTP cria o checker HTTP.
func NewHTTP() *HTTP {
	secure := http.DefaultTransport.(*http.Transport).Clone()
	insecure := http.DefaultTransport.(*http.Transport).Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opção explícita por monitor

	return &HTTP{
		secure: secure, insecure: insecure,
		restrictedSecure:   restrictedTransport(secure),
		restrictedInsecure: restrictedTransport(insecure),
		compiled:           map[int64]compiledConfig{},
	}
}

// restrictedTransport deriva o transporte dos monitores com
// RestrictInternal.
//
// A conferência fica no dialer, e não na política de redirecionamento:
// cada salto é uma conexão nova e passa por ela com o endereço que vai ser
// de fato conectado, enquanto conferir a URL do salto resolveria o nome
// uma vez para conferir e outra para conectar.
//
// Sem proxy: atrás de um, quem conecta ao destino é o proxy, e o servidor
// não teria como saber para onde. Monitor restrito vai direto, ou a regra
// viraria enfeite em toda instalação com HTTPS_PROXY.
func restrictedTransport(base *http.Transport) *http.Transport {
	t := base.Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialerFor(domain.Monitor{RestrictInternal: true}).DialContext(ctx, network, addr)
	}
	return t
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
		if len(cfg.BodyRegex) > MaxBodyRegexLength {
			return fmt.Errorf("checker: body_regex tem %d caracteres; o máximo é %d",
				len(cfg.BodyRegex), MaxBodyRegexLength)
		}
		if _, err := regexp.Compile(cfg.BodyRegex); err != nil {
			return fmt.Errorf("checker: expressão regular inválida em body_regex: %w", err)
		}
	}
	if len(cfg.JSONAssertions) > MaxJSONAssertions {
		return fmt.Errorf("checker: json_assertions tem %d condições; o máximo é %d",
			len(cfg.JSONAssertions), MaxJSONAssertions)
	}
	if _, err := compileAssertions(cfg.JSONAssertions); err != nil {
		return err
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
	compiled, err := h.compiledFor(m)
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
	resp, err := h.client(cfg, m.RestrictInternal).Do(req)
	latency := time.Since(start).Milliseconds()
	if refused, ok := internalRefusal(err); ok {
		return refused
	}
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

	if msg := evaluateBody(cfg, compiled.bodyRegex, string(payload)); msg != "" {
		res.Status = domain.StatusDown
		res.Message = msg
		return res
	}

	if len(compiled.assertions) > 0 {
		// Corpo do tamanho exato do teto quase certamente foi cortado; a
		// causa precisa dizer isso em vez de culpar o JSON do alvo.
		truncated := len(payload) == MaxBodyBytes
		if msg := evaluateJSONAssertions(compiled.assertions, payload, truncated); msg != "" {
			res.Status = domain.StatusDown
			res.Message = msg
			return res
		}
	}

	res.Status = domain.StatusUp
	return res
}

// compiledFor devolve as asserções JSON e a expressão regular do monitor
// já interpretadas. Interpretar é trabalho do cadastro; o Check roda a
// cada poucos segundos e só repete o trabalho quando a configuração muda.
//
// A expressão regular entrou aqui pelo mesmo motivo das asserções: ela era
// compilada dentro da avaliação do corpo, isto é, a cada verificação. Um
// monitor de 10 segundos compilava o mesmo padrão 8.640 vezes por dia, e o
// custo é do tamanho do padrão, não do corpo — quem escreve a expressão
// mais cara paga mais, para sempre.
func (h *HTTP) compiledFor(m domain.Monitor) (compiledConfig, error) {
	h.mu.Lock()
	cached, ok := h.compiled[m.ID]
	h.mu.Unlock()
	if ok && cached.config == string(m.Config) {
		return cached, nil
	}

	cfg, err := parseHTTPConfig(m.Config)
	if err != nil {
		return compiledConfig{}, err
	}
	assertions, err := compileAssertions(cfg.JSONAssertions)
	if err != nil {
		return compiledConfig{}, err
	}
	var bodyRegex *regexp.Regexp
	if cfg.BodyRegex != "" {
		if bodyRegex, err = regexp.Compile(cfg.BodyRegex); err != nil {
			return compiledConfig{}, fmt.Errorf("expressão regular inválida em body_regex: %w", err)
		}
	}

	entrada := compiledConfig{
		config:     string(m.Config),
		assertions: assertions,
		bodyRegex:  bodyRegex,
	}
	h.mu.Lock()
	h.compiled[m.ID] = entrada
	h.mu.Unlock()
	return entrada, nil
}

// client monta um cliente com o timeout e a política de redirecionamento
// do monitor, reaproveitando o transporte compartilhado.
func (h *HTTP) client(cfg HTTPConfig, restrict bool) *http.Client {
	transport := h.secure
	switch {
	case restrict && cfg.SkipTLSVerify:
		transport = h.restrictedInsecure
	case restrict:
		transport = h.restrictedSecure
	case cfg.SkipTLSVerify:
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
//
// A expressão regular vem compilada de fora: ela é a mesma a cada
// verificação do mesmo monitor, e compilá-la aqui seria pagar o cadastro
// de novo em toda batida. Nula quando o monitor não tem body_regex.
func evaluateBody(cfg HTTPConfig, bodyRegex *regexp.Regexp, body string) string {
	if cfg.BodyContains != "" && !strings.Contains(body, cfg.BodyContains) {
		return fmt.Sprintf("corpo não contém %q", cfg.BodyContains)
	}
	if cfg.BodyNotContains != "" && strings.Contains(body, cfg.BodyNotContains) {
		return fmt.Sprintf("corpo contém o termo proibido %q", cfg.BodyNotContains)
	}
	if bodyRegex != nil && !bodyRegex.MatchString(body) {
		return fmt.Sprintf("corpo não casa com %q", cfg.BodyRegex)
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
