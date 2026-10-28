package api_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/domain"
)

// O sinal de push com estado, causa e latência.
//
// Quem reporta por push muitas vezes sabe o que deu errado — um script que
// confere uma fila e encontra zero consumidores, um agente atrás de VPN
// que roda verificações locais. Estes testes fixam o contrato que deixa
// esse conhecimento chegar até o aviso.

const pushToken = "um-token-suficientemente-longo"

// newPushServer sobe a API com um monitor push e devolve o seu id. A
// sessão é descartada: quem reporta é um cron, sem credencial de
// interface.
func newPushServer(t *testing.T) (*server, int64) {
	t.Helper()
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, pushMonitorBody("fila de pedidos", pushToken))
	s.cookie = nil
	return s, id
}

// lastSignal lê o que ficou gravado do último sinal.
func (s *server) lastSignal(t *testing.T, id int64) domain.PushSignal {
	t.Helper()
	sig, ok, err := s.store.LastPush(context.Background(), id)
	if err != nil {
		t.Fatalf("LastPush returned unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("no push was recorded")
	}
	return sig
}

// rawPush envia o corpo como está, com o tipo de conteúdo dado.
func (s *server) rawPush(t *testing.T, method, query, contentType, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method,
		s.URL+"/api/v1/push/"+pushToken+query, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest returned unexpected error: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request returned unexpected error: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// Muito agendador e muito healthcheck só sabe fazer `curl URL`, e o push
// existe justamente para eles.
func TestPushAcceptsGet(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.do(t, http.MethodGet, "/api/v1/push/"+pushToken, nil)

	assertStatus(t, resp, http.StatusOK)
	if got := s.lastSignal(t, id); !got.At.Equal(epoch) {
		t.Errorf("push recorded at %v, want %v", got.At, epoch)
	}
}

// O sinal sem parâmetros continua significando "estou vivo": quem já usa
// o push não pode ter de mudar o script.
func TestPushWithoutParametersMeansUp(t *testing.T) {
	s, id := newPushServer(t)

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/"+pushToken, nil), http.StatusOK)

	got := s.lastSignal(t, id)
	if got.Status != domain.StatusUp || got.Message != "" || got.LatencyMS != 0 {
		t.Errorf("signal = %+v, want a bare up", got)
	}
}

// O caso da issue: o script que encontra a fila parada diz isso, em vez
// de escolher entre mentir e calar.
func TestPushRecordsStatusCauseAndLatencyFromQuery(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.do(t, http.MethodPost,
		"/api/v1/push/"+pushToken+"?status=down&msg=fila+sem+worker&latency_ms=840", nil)

	assertStatus(t, resp, http.StatusOK)
	got := s.lastSignal(t, id)
	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if got.Message != "fila sem worker" {
		t.Errorf("Message = %q, want %q", got.Message, "fila sem worker")
	}
	if got.LatencyMS != 840 {
		t.Errorf("LatencyMS = %d, want 840", got.LatencyMS)
	}
}

func TestPushRecordsStatusCauseAndLatencyFromJSONBody(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.rawPush(t, http.MethodPost, "", "application/json",
		`{"status":"degraded","msg":"fila com 5 mil itens","latency_ms":1200}`)

	assertStatus(t, resp, http.StatusOK)
	got := s.lastSignal(t, id)
	if got.Status != domain.StatusDegraded || got.Message != "fila com 5 mil itens" || got.LatencyMS != 1200 {
		t.Errorf("signal = %+v, want degraded with the reported cause and latency", got)
	}
}

// Um agente que mande campos a mais (versão, hostname) não pode ter o
// sinal recusado: o que importa é chegar o estado, e recusar faria o
// monitor ficar em silêncio por causa de um detalhe de formato.
func TestPushToleratesExtraJSONFields(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.rawPush(t, http.MethodPost, "", "application/json",
		`{"status":"down","msg":"disco cheio","agent":"v1.2"}`)

	assertStatus(t, resp, http.StatusOK)
	if got := s.lastSignal(t, id); got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// Corpo sem tipo JSON é ignorado, como sempre foi: um cron que já fazia
// POST com qualquer coisa no corpo não pode passar a ser recusado.
func TestPushIgnoresNonJSONBody(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.rawPush(t, http.MethodPost, "", "text/plain", "backup ok")

	assertStatus(t, resp, http.StatusOK)
	if got := s.lastSignal(t, id); got.Status != domain.StatusUp {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusUp)
	}
}

// Status inválido é erro do script, e precisa voltar como erro para quem
// escreveu o script perceber; gravá-lo como "up" esconderia a falha que
// ele estava tentando reportar.
func TestPushRejectsInvalidStatus(t *testing.T) {
	s, id := newPushServer(t)

	resp := s.do(t, http.MethodPost, "/api/v1/push/"+pushToken+"?status=fora", nil)

	assertStatus(t, resp, http.StatusBadRequest)
	if _, ok, _ := s.store.LastPush(context.Background(), id); ok {
		t.Error("an invalid signal was recorded")
	}
}

func TestPushRejectsInvalidLatency(t *testing.T) {
	s, _ := newPushServer(t)

	resp := s.do(t, http.MethodPost, "/api/v1/push/"+pushToken+"?latency_ms=-5", nil)

	assertStatus(t, resp, http.StatusBadRequest)
}

func TestPushRejectsMalformedJSON(t *testing.T) {
	s, _ := newPushServer(t)

	resp := s.rawPush(t, http.MethodPost, "", "application/json", `{"status":`)

	assertStatus(t, resp, http.StatusBadRequest)
}

// O token é conferido antes dos parâmetros: validar primeiro deixaria quem
// não tem token sondar o contrato e distinguir respostas.
func TestPushChecksTokenBeforeParameters(t *testing.T) {
	s, _ := newPushServer(t)

	resp := s.do(t, http.MethodPost, "/api/v1/push/token-errado-mas-comprido?status=fora", nil)

	assertStatus(t, resp, http.StatusUnauthorized)
}

// A mensagem é dado de fora e vai parar em aviso de chat. Cortá-la em vez
// de recusar mantém o sinal — que é o que importa — e impede que um log
// inteiro chegue em cada notificação.
func TestPushTruncatesLongMessage(t *testing.T) {
	s, id := newPushServer(t)

	long := strings.Repeat("a", domain.MaxPushMessageLength*3)
	resp := s.do(t, http.MethodPost, "/api/v1/push/"+pushToken+"?status=down&msg="+long, nil)

	assertStatus(t, resp, http.StatusOK)
	msg := s.lastSignal(t, id).Message
	if msg == "" {
		t.Fatal("the message was dropped, want it kept and truncated")
	}
	if n := utf8.RuneCountInString(msg); n > domain.MaxPushMessageLength {
		t.Errorf("message has %d characters, want at most %d", n, domain.MaxPushMessageLength)
	}
}

// ---------- agenda ----------

// A próxima execução esperada aparece no detalhe do monitor: é assim que
// uma expressão ou um fuso errados aparecem no cadastro, e não na primeira
// madrugada em que o aviso deveria ter chegado.
func TestMonitorDetailShowsNextExpectedPush(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	body := pushMonitorBody("backup noturno", pushToken)
	body["config"] = map[string]any{
		"token": pushToken, "schedule": "2 9,18 * * *",
		"timezone": "America/Sao_Paulo", "grace_period_seconds": 600,
	}
	id := s.createMonitor(t, body)

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusOK)

	got := decode[struct {
		NextExpectedPush *time.Time `json:"next_expected_push"`
	}](t, resp)
	if got.NextExpectedPush == nil {
		t.Fatal("next_expected_push is missing from the monitor detail")
	}
	// epoch é 12:00 UTC, 09:00 em São Paulo: a próxima é 09:02 local.
	if want := time.Date(2026, 7, 28, 12, 2, 0, 0, time.UTC); !got.NextExpectedPush.Equal(want) {
		t.Errorf("next_expected_push = %v, want %v", got.NextExpectedPush.UTC(), want)
	}
}

// Sem agenda não há execução prevista; inventar uma data faria a interface
// prometer algo que o monitor não vigia.
func TestMonitorDetailOmitsNextExpectedPushWithoutSchedule(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, pushMonitorBody("cron", pushToken))

	resp := s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(id), nil)
	assertStatus(t, resp, http.StatusOK)

	if strings.Contains(readBody(t, resp), "next_expected_push") {
		t.Error("next_expected_push present on a monitor without schedule")
	}
}

func TestCreatePushMonitorRejectsBadSchedule(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	body := pushMonitorBody("backup noturno", pushToken)
	body["config"] = map[string]any{"token": pushToken, "schedule": "2 9 * *", "timezone": "UTC"}

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", body)

	assertStatus(t, resp, http.StatusBadRequest)
}

// O monitor entregue ao agendador depois de uma edição precisa manter a
// data de criação: é dela que o push conta a primeira janela, e sem ela o
// monitor que nunca recebeu sinal ficaria "aguardando" para sempre só
// porque alguém mudou o nome.
func TestUpdateMonitorKeepsCreationTime(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, pushMonitorBody("cron", pushToken))

	s.clock.Advance(time.Hour)
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), pushMonitorBody("cron renomeado", pushToken))
	assertStatus(t, resp, http.StatusOK)

	got := decode[struct {
		CreatedAt time.Time `json:"created_at"`
	}](t, resp)
	if got.CreatedAt.IsZero() {
		t.Error("created_at is zero after an update, want the original creation time")
	}
}

// ---------- superfície pública ----------

// A causa informada pelo push é dado de fora, escrito por quem tem o
// token — e quem tem o token não é necessariamente quem decide o que o
// cliente lê. Pelo mesmo motivo que a causa detectada não é publicada,
// esta também não pode ser: um script que reporta "senha do banco
// expirada em db-interno-03" não pode transformar a página de estado em
// vitrine da infraestrutura.
func TestPublicPageDoesNotLeakPushCause(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	const causa = "senha expirada em db-interno-03"

	monitorID := s.createMonitor(t, pushMonitorBody("fila-interna-pedidos", pushToken))
	resp := s.do(t, http.MethodPost, "/api/v1/status-pages", map[string]any{
		"slug": "estado", "title": "Estado da plataforma",
	})
	assertStatus(t, resp, http.StatusCreated)
	paginaID := decode[idResp](t, resp).ID
	resp = s.do(t, http.MethodPut,
		"/api/v1/status-pages/"+itoa(paginaID)+"/components/"+itoa(monitorID),
		map[string]any{"label": "Pedidos"})
	assertStatus(t, resp, http.StatusOK)

	sessao := s.cookie
	s.cookie = nil
	resp = s.do(t, http.MethodGet,
		"/api/v1/push/"+pushToken+"?status=down&msg="+strings.ReplaceAll(causa, " ", "+"), nil)
	assertStatus(t, resp, http.StatusOK)

	// O que o agendador e o motor fariam: a verificação vira batida, e a
	// queda confirmada vira incidente com a causa.
	ctx := context.Background()
	m, err := s.store.Monitors().Get(ctx, monitorID)
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	res := checker.NewPush(s.store, s.clock).Check(ctx, m)
	if err := s.store.WriteHeartbeats(ctx, []domain.Heartbeat{{
		MonitorID: monitorID, Timestamp: s.clock.Now(), Status: res.Status,
		Message: res.Message, LatencyMS: res.LatencyMS, ProbeID: domain.DefaultProbeID,
	}}); err != nil {
		t.Fatalf("WriteHeartbeats returned unexpected error: %v", err)
	}
	if err := s.store.Incidents().Open(ctx, &domain.Incident{
		MonitorID: monitorID, StartedAt: s.clock.Now(), Cause: res.Message,
	}); err != nil {
		t.Fatalf("Open returned unexpected error: %v", err)
	}

	// Controle positivo: a causa existe por dentro. Sem isto o teste
	// passaria também se o push simplesmente perdesse a mensagem.
	s.cookie = sessao
	resp = s.do(t, http.MethodGet, "/api/v1/monitors/"+itoa(monitorID)+"/heartbeats?from=2026-07-28T11:00:00Z&to=2026-07-28T13:00:00Z", nil)
	assertStatus(t, resp, http.StatusOK)
	if !strings.Contains(readBody(t, resp), causa) {
		t.Fatal("the reported cause did not reach the heartbeats; the leak check below would be vacuous")
	}

	s.cookie = nil
	for _, caminho := range []string{"/api/v1/public/estado", "/api/v1/public/estado/feed.atom"} {
		resp = s.do(t, http.MethodGet, caminho, nil)
		assertStatus(t, resp, http.StatusOK)
		corpo := readBody(t, resp)
		for _, agulha := range []string{causa, "db-interno-03", "fila-interna-pedidos"} {
			if strings.Contains(corpo, agulha) {
				t.Errorf("%s revelou %q:\n%s", caminho, agulha, corpo)
			}
		}
	}
}

// ---------- token: unicidade e abuso ----------

// Dois monitores com o mesmo token fariam o sinal de um cair no outro, e o
// que ficasse sem sinal cairia por silêncio com o serviço no ar. O
// cadastro recusa o token repetido no campo da configuração, onde o
// administrador sabe o que corrigir.
func TestCreatePushMonitorRejectsTakenToken(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, pushMonitorBody("cron A", pushToken))

	resp := s.do(t, http.MethodPost, "/api/v1/monitors", pushMonitorBody("cron B", pushToken))

	assertStatus(t, resp, http.StatusBadRequest)
	if body := readBody(t, resp); !strings.Contains(body, "config") {
		t.Errorf("error body = %s, want it to point at the config field", body)
	}
}

func TestUpdatePushMonitorRejectsTakenToken(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	s.createMonitor(t, pushMonitorBody("cron A", pushToken))
	id := s.createMonitor(t, pushMonitorBody("cron B", "outro-token-bem-comprido"))

	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), pushMonitorBody("cron B", pushToken))

	assertStatus(t, resp, http.StatusBadRequest)
}

// Salvar o monitor sem trocar o token não pode esbarrar nele mesmo.
func TestUpdatePushMonitorKeepsItsOwnToken(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, pushMonitorBody("cron", pushToken))

	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), pushMonitorBody("cron renomeado", pushToken))

	assertStatus(t, resp, http.StatusOK)
}

// Trocar o token na edição troca o que o /push aceita. Se a busca pelo
// dono olhasse uma cópia que não acompanha a edição, o token vazado
// continuaria valendo depois de trocado.
func TestPushFollowsTokenChange(t *testing.T) {
	s := newServer(t)
	s.setup(t)
	id := s.createMonitor(t, pushMonitorBody("cron", pushToken))
	const novo = "token-novo-depois-do-vazamento"
	resp := s.do(t, http.MethodPut, "/api/v1/monitors/"+itoa(id), pushMonitorBody("cron", novo))
	assertStatus(t, resp, http.StatusOK)
	s.cookie = nil

	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/"+pushToken, nil), http.StatusUnauthorized)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/"+novo, nil), http.StatusOK)
}

// O /push não tem login. Sem limite, qualquer um mantém o servidor
// ocupado conferindo tokens — ou tenta adivinhar um. O limite é por
// origem e generoso o bastante para uma máquina com dezenas de crons; o
// excesso recebe 429, que diz a um cliente honesto para esperar.
func TestPushIsRateLimitedPerClient(t *testing.T) {
	s, _ := newPushServer(t)

	limited := false
	for range 1000 {
		resp := s.do(t, http.MethodGet, "/api/v1/push/token-errado-mas-comprido", nil)
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			if resp.Header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After, want a hint of when to retry")
			}
			break
		}
	}
	if !limited {
		t.Fatal("1000 pushes in a row were never limited")
	}

	// O limite vale também para o token certo: quem estourou espera.
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/"+pushToken, nil), http.StatusTooManyRequests)

	// E passa com o tempo, sem precisar reiniciar nada.
	s.clock.Advance(time.Minute)
	assertStatus(t, s.do(t, http.MethodPost, "/api/v1/push/"+pushToken, nil), http.StatusOK)
}
