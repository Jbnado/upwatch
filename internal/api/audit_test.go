package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Jbnado/upwatch/internal/api"
)

// registro captura o log da API sob teste.
//
// Com trava porque o servidor atende em outra goroutine: ler o buffer
// enquanto o handler escreve nele é corrida, e a suíte roda com -race.
type registro struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *registro) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *registro) texto() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// linhas devolve as linhas de auditoria, que são as que trazem "ação=".
func (r *registro) linhas() []string {
	var out []string
	for _, l := range strings.Split(r.texto(), "\n") {
		if strings.Contains(l, "ação=") {
			out = append(out, l)
		}
	}
	return out
}

// comAção devolve a primeira linha de auditoria da ação pedida.
func (r *registro) comAção(t *testing.T, ação string) string {
	t.Helper()

	for _, l := range r.linhas() {
		if strings.Contains(l, "ação="+ação) {
			return l
		}
	}
	t.Fatalf("nenhuma linha de auditoria com ação=%s; log:\n%s", ação, r.texto())
	return ""
}

// recusaAção falha se alguma linha de auditoria tiver a ação pedida.
func (r *registro) recusaAção(t *testing.T, ação string) {
	t.Helper()

	for _, l := range r.linhas() {
		if strings.Contains(l, "ação="+ação) {
			t.Fatalf("linha de auditoria inesperada com ação=%s: %s", ação, l)
		}
	}
}

// exigeCampos confere que a linha traz ator, alvo e origem preenchidos.
func exigeCampos(t *testing.T, linha string, ator, alvo, origem string) {
	t.Helper()

	for campo, valor := range map[string]string{
		"ator": ator, "alvo": alvo, "origem": origem,
	} {
		if valor == "" {
			continue
		}
		if !strings.Contains(linha, campo+"="+valor) {
			t.Errorf("linha não traz %s=%s: %s", campo, valor, linha)
		}
	}
}

// atorAnônimoNoLog espelha o ator que o pacote usa sem conta
// identificada. Repetido aqui de propósito: o teste falha se o valor
// mudar sem alguém notar, e é esse valor que quem lê o log procura.
const atorAnônimoNoLog = "anônimo"

// servidorComLog sobe a API escrevendo o log no registro devolvido.
func servidorComLog(t *testing.T, ajuste func(*api.Options)) (*server, *registro) {
	t.Helper()

	reg := &registro{}
	s := newServerWith(t, func(o *api.Options) {
		o.Logger = slog.New(slog.NewTextHandler(reg, nil))
		if ajuste != nil {
			ajuste(o)
		}
	})
	return s, reg
}

// ---------- login ----------

// Sem esta linha, uma força bruta contra a senha não deixa rastro: o
// bloqueio por conta acontece, mas ninguém consegue saber depois que ele
// aconteceu, de onde, nem contra qual conta.
func TestAuditLogsFailedLogin(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "senha-errada-de-proposito",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "login.senha_errada")
	exigeCampos(t, linha, "admin", "", "127.0.0.1")
}

// O 429 é a única evidência de que alguém insistiu o bastante para
// esgotar o balde da origem. Sem registro, o bloqueio age e não conta a
// ninguém que agiu.
func TestAuditLogsRateLimitBlock(t *testing.T) {
	s, reg := servidorComLog(t, nil)

	// Conta diferente a cada palpite, como faz quem espalha a força bruta
	// para não bater no bloqueio de nenhuma: é esse caso que o limite por
	// origem existe para pegar, e é o 429 dele que queremos registrado.
	var last *http.Response
	for i := 0; i <= 100; i++ {
		last = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
			"username": "conta-" + strconv.Itoa(i), "password": "senha-errada-porem-longa",
		})
		if last.StatusCode == http.StatusTooManyRequests {
			break
		}
	}
	assertStatus(t, last, http.StatusTooManyRequests)

	linha := reg.comAção(t, "limite.bloqueio")
	exigeCampos(t, linha, atorAnônimoNoLog, "/api/v1/auth/login", "127.0.0.1")
}

// O 429 por conta é outro evento que o por origem: um diz que um endereço
// está insistindo, o outro que uma conta específica está sob ataque. Quem
// lê o log depois precisa saber qual dos dois aconteceu.
func TestAuditLogsAccountLockout(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	var last *http.Response
	for i := 0; i <= 100; i++ {
		last = s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
			"username": "admin", "password": "senha-errada-porem-longa",
		})
		if last.StatusCode == http.StatusTooManyRequests {
			break
		}
	}
	assertStatus(t, last, http.StatusTooManyRequests)

	linha := reg.comAção(t, "login.conta_bloqueada")
	exigeCampos(t, linha, "admin", "", "127.0.0.1")

	// O bloqueio por conta não é o por origem: confundir os dois faria o
	// log dizer que um endereço foi barrado quando foi uma conta.
	reg.recusaAção(t, "limite.bloqueio")
}

// Código errado é a tentativa de passar o segundo fator com a senha já
// na mão: o sinal mais forte de conta comprometida que o sistema produz.
func TestAuditLogsWrongTwoFactorCode(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)
	s.enroll(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": adminPassword,
	})
	assertStatus(t, resp, http.StatusOK)
	desafio := decode[map[string]any](t, resp)["challenge"].(string)

	resp = s.do(t, http.MethodPost, "/api/v1/auth/login/2fa", map[string]any{
		"challenge": desafio, "code": "000000",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "login.codigo_errado")
	exigeCampos(t, linha, "admin", "", "127.0.0.1")
}

// ---------- credenciais programáticas ----------

// Token de API é credencial de longa duração: quem o emite e quando
// precisa ficar registrado, ou um token plantado passa por legítimo.
func TestAuditLogsTokenCreated(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{
		"name": "token-do-ci",
	})
	assertStatus(t, resp, http.StatusCreated)

	linha := reg.comAção(t, "token.criado")
	exigeCampos(t, linha, "admin", "token-do-ci", "127.0.0.1")
}

func TestAuditLogsTokenRevoked(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{
		"name": "token-a-revogar",
	})
	assertStatus(t, resp, http.StatusCreated)
	criado := decode[map[string]any](t, resp)
	id := strconv.FormatInt(int64(criado["id"].(float64)), 10)

	resp = s.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+id, nil)
	assertStatus(t, resp, http.StatusNoContent)

	linha := reg.comAção(t, "token.revogado")
	exigeCampos(t, linha, "admin", id, "127.0.0.1")
}

// ---------- conta ----------

func TestAuditLogsPasswordChanged(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"current_password": adminPassword, "new_password": "outra-senha-bem-longa",
	})
	assertStatus(t, resp, http.StatusNoContent)

	linha := reg.comAção(t, "senha.trocada")
	exigeCampos(t, linha, "admin", "admin", "127.0.0.1")
}

func TestAuditLogsUserCreated(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "observadora", "password": "uma-senha-bem-longa", "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)

	linha := reg.comAção(t, "usuario.criado")
	exigeCampos(t, linha, "admin", "observadora", "127.0.0.1")
}

// Promover alguém a admin, ou alargar o escopo dele, é a mudança de
// permissão que um invasor faz para se manter dentro. Sem linha, ela
// acontece sem rastro.
func TestAuditLogsRoleChanged(t *testing.T) {
	s, reg := servidorComLog(t, nil)
	s.setup(t)

	resp := s.do(t, http.MethodPost, "/api/v1/users", map[string]any{
		"username": "observadora", "password": "uma-senha-bem-longa", "role": "viewer",
	})
	assertStatus(t, resp, http.StatusCreated)
	id := strconv.FormatInt(int64(decode[map[string]any](t, resp)["id"].(float64)), 10)

	resp = s.do(t, http.MethodPut, "/api/v1/users/"+id, map[string]any{"role": "admin"})
	assertStatus(t, resp, http.StatusOK)

	linha := reg.comAção(t, "usuario.acesso_trocado")
	exigeCampos(t, linha, "admin", "observadora", "127.0.0.1")
}

// ---------- tokens de configuração e de métricas ----------

// O token de configuração entrega a instalação a quem acertá-lo. Errar
// é o sinal de que alguém está tentando.
func TestAuditLogsWrongSetupToken(t *testing.T) {
	s, reg := servidorComLog(t, nil)

	resp := s.do(t, http.MethodPost, "/api/v1/setup", map[string]any{
		"username": "invasor", "password": "uma-senha-bem-longa",
		"setup_token": "token-errado-de-proposito",
	})
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "setup.token_errado")
	exigeCampos(t, linha, atorAnônimoNoLog, "/setup", "127.0.0.1")
}

func TestAuditLogsWrongMetricsToken(t *testing.T) {
	s, reg := servidorComLog(t, func(o *api.Options) {
		o.MetricsToken = "token-de-metricas-dos-testes"
	})

	req, err := http.NewRequest(http.MethodGet, s.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	req.Header.Set("Authorization", "Bearer token-errado-de-proposito")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	defer resp.Body.Close()
	assertStatus(t, resp, http.StatusUnauthorized)

	linha := reg.comAção(t, "metrics.token_errado")
	exigeCampos(t, linha, atorAnônimoNoLog, "/metrics", "127.0.0.1")
}
