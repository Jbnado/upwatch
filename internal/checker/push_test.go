package checker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jbnado/upwatch/internal/checker"
	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
)

var pushNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// pushLog devolve o último sinal, controlado pelo teste.
//
// Sem status explícito o sinal é "up", como o de um cron que só diz
// "estou vivo".
type pushLog struct {
	at      time.Time
	has     bool
	err     error
	status  domain.Status
	message string
	latency int64
}

func (p *pushLog) LastPush(context.Context, int64) (domain.PushSignal, bool, error) {
	status := p.status
	if status == domain.StatusUnknown {
		status = domain.StatusUp
	}
	return domain.PushSignal{
		At: p.at, Status: status, Message: p.message, LatencyMS: p.latency,
	}, p.has, p.err
}

func pushMonitor(t *testing.T, interval time.Duration, cfg map[string]any) domain.Monitor {
	t.Helper()

	raw := json.RawMessage(`{"token":"segredo-de-16-chars"}`)
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshalling config returned unexpected error: %v", err)
		}
		raw = b
	}
	return domain.Monitor{
		ID: 1, Name: "cron noturno", Type: domain.MonitorPush,
		Interval: interval, Timeout: interval / 2,
		ConfirmationThreshold: 1, Config: raw,
		CreatedAt: pushNow.Add(-30 * time.Second),
	}
}

func newPush(log *pushLog) *checker.Push {
	return checker.NewPush(log, clock.NewFake(pushNow))
}

func TestPushTypeIsPush(t *testing.T) {
	if got := newPush(&pushLog{}).Type(); got != domain.MonitorPush {
		t.Errorf("Type() = %v, want %v", got, domain.MonitorPush)
	}
}

func TestPushWithinWindowIsUp(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-30 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// A janela é o intervalo mais a folga: um cron que atrasa alguns segundos
// não pode disparar alerta, senão o monitor vira fonte de ruído.
func TestPushToleratesDelayWithinGracePeriod(t *testing.T) {
	// Intervalo de 60s com folga padrão de mais 60s: 90s de silêncio ainda
	// está dentro.
	log := &pushLog{at: pushNow.Add(-90 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

func TestPushBeyondWindowIsDown(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-5 * time.Minute), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "sem sinal") {
		t.Errorf("Message = %q, want it to say no signal arrived", got.Message)
	}
}

func TestPushHonoursExplicitGracePeriod(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-100 * time.Second), has: true}
	cfg := map[string]any{"token": "segredo-de-16-chars", "grace_period_seconds": 300}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, cfg))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Monitor recém-criado ainda não recebeu nada. Marcar como fora do ar
// dispararia alerta antes de o serviço ter tido chance de reportar.
func TestPushNeverSignalledIsUnknownWithinTheFirstWindow(t *testing.T) {
	got := newPush(&pushLog{has: false}).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusUnknown {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUnknown)
	}
}

// O push é um dead-man's switch: se a sonda nunca sobe, alguém precisa
// saber. Ficar em "aguardando" para sempre deixaria justamente a falha de
// implantação — script nunca agendado, token errado no agente — sem aviso
// nenhum.
func TestPushNeverSignalledBeyondTheFirstWindowIsDown(t *testing.T) {
	m := pushMonitor(t, time.Minute, nil)
	// Janela padrão de 2min (intervalo mais folga igual); criado há 3min.
	m.CreatedAt = pushNow.Add(-3 * time.Minute)

	got := newPush(&pushLog{has: false}).Check(context.Background(), m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDown)
	}
	if got.Message != "nenhum sinal recebido desde a criação do monitor" {
		t.Errorf("Message = %q, want it to say no signal ever arrived", got.Message)
	}
}

func TestPushStoreErrorIsDown(t *testing.T) {
	log := &pushLog{err: errors.New("banco indisponível")}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
}

// O tempo desde o último sinal é o que o operador olha para decidir se um
// cron está apenas atrasado ou realmente parado.
func TestPushReportsSecondsSinceLastSignal(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-45 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Meta["seconds_since_push"] != "45" {
		t.Errorf("Meta[seconds_since_push] = %q, want %q", got.Meta["seconds_since_push"], "45")
	}
	if got.Meta["last_push"] == "" {
		t.Error("Meta[last_push] is empty, want the instant of the last signal")
	}
}

// Sem medição informada, a latência fica zerada: o UpWatch não mede tempo
// de resposta de nada num monitor push, e inventar um número poluiria os
// percentis.
func TestPushReportsNoLatencyWhenTheServiceMeasuredNone(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-10 * time.Second), has: true}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.LatencyMS != 0 {
		t.Errorf("LatencyMS = %d, want 0: a push monitor measures no round trip", got.LatencyMS)
	}
}

// Quando o próprio serviço mede quanto a tarefa levou, esse é o único
// número de duração que existe; descartá-lo esconderia a rotina que vai
// ficando lenta até estourar o horário.
func TestPushReportsTheLatencyMeasuredByTheService(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-10 * time.Second), has: true, latency: 842}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.LatencyMS != 842 {
		t.Errorf("LatencyMS = %d, want 842 as reported by the service", got.LatencyMS)
	}
}

// Quem reporta por push muitas vezes sabe o que deu errado. Um "down"
// explícito precisa virar falha com a causa informada; senão o script teria
// de calar e esperar a janela estourar, e o aviso chegaria atrasado
// dizendo só "silêncio".
func TestPushReportedDownCarriesTheCause(t *testing.T) {
	log := &pushLog{
		at: pushNow.Add(-10 * time.Second), has: true,
		status: domain.StatusDown, message: "fila sem worker",
	}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(got.Message, "fila sem worker") {
		t.Errorf("Message = %q, want the cause reported by the service", got.Message)
	}
}

// Queda sem causa ainda precisa dizer de onde veio a falha: sem isso o
// operador procuraria um problema de rede que não existe.
func TestPushReportedDownWithoutCauseSaysTheServiceReportedIt(t *testing.T) {
	log := &pushLog{at: pushNow.Add(-10 * time.Second), has: true, status: domain.StatusDown}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "reportou") {
		t.Errorf("Message = %q, want it to say the service itself reported the failure", got.Message)
	}
}

func TestPushReportedDegradedIsDegraded(t *testing.T) {
	log := &pushLog{
		at: pushNow.Add(-10 * time.Second), has: true,
		status: domain.StatusDegraded, message: "fila com 5 mil itens",
	}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDegraded {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDegraded)
	}
	if got.Message != "fila com 5 mil itens" {
		t.Errorf("Message = %q, want the reported cause", got.Message)
	}
}

// Silêncio vence o último status: um "up" antigo não pode manter verde um
// serviço que parou de reportar, e um "down" antigo não pode esconder que
// agora o problema é outro — ninguém mais está reportando.
func TestPushSilenceOverridesTheLastReportedStatus(t *testing.T) {
	log := &pushLog{
		at: pushNow.Add(-5 * time.Minute), has: true,
		status: domain.StatusDown, message: "fila sem worker",
	}

	got := newPush(log).Check(context.Background(), pushMonitor(t, time.Minute, nil))

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v, want %v", got.Status, domain.StatusDown)
	}
	if !strings.Contains(strings.ToLower(got.Message), "sem sinal") {
		t.Errorf("Message = %q, want it to report the silence", got.Message)
	}
}

func TestPushValidateConfigRequiresToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig without a token returned nil error, want an error")
	}
}

// Token curto é adivinhável, e quem adivinha consegue manter um monitor
// artificialmente saudável enquanto o serviço real está parado.
func TestPushValidateConfigRejectsShortToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"token": "curto"})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a short token returned nil error, want an error")
	}
}

func TestPushValidateConfigAcceptsProperToken(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{"token": "um-token-suficientemente-longo"})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig returned unexpected error: %v", err)
	}
}

func TestPushValidateConfigRejectsNegativeGrace(t *testing.T) {
	cfg, _ := json.Marshal(map[string]any{
		"token": "um-token-suficientemente-longo", "grace_period_seconds": -1,
	})

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig with a negative grace period returned nil error, want an error")
	}
}

func TestPushValidateConfigRejectsMalformedJSON(t *testing.T) {
	if err := newPush(&pushLog{}).ValidateConfig(json.RawMessage("{nope")); err == nil {
		t.Fatal("ValidateConfig of malformed JSON returned nil error, want an error")
	}
}

func TestPushGenerateTokenProducesDistinctSecrets(t *testing.T) {
	first, err := checker.GeneratePushToken()
	if err != nil {
		t.Fatalf("GeneratePushToken returned unexpected error: %v", err)
	}
	second, err := checker.GeneratePushToken()
	if err != nil {
		t.Fatalf("GeneratePushToken returned unexpected error: %v", err)
	}

	if first == second {
		t.Error("GeneratePushToken returned the same value twice, want unpredictable secrets")
	}
	if len(first) < checker.MinPushTokenLength {
		t.Errorf("generated token has %d characters, want at least %d",
			len(first), checker.MinPushTokenLength)
	}
}

// ---------- agenda cron ----------

// mustLocation carrega um fuso da base IANA. São Paulo não tem horário de
// verão desde 2019, o que deixa os testes de agenda livres de
// deslocamento; o de horário de verão usa Nova York.
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q) returned unexpected error: %v", name, err)
	}
	return loc
}

// scheduledMonitor monta o caso da issue: rotina das 09:02 e 18:02, com
// folga de 10 minutos, verificada a cada minuto.
func scheduledMonitor(t *testing.T, tz string) domain.Monitor {
	t.Helper()
	return pushMonitor(t, time.Minute, map[string]any{
		"token": "segredo-de-16-chars", "schedule": "2 9,18 * * *",
		"timezone": tz, "grace_period_seconds": 600,
	})
}

func checkAt(t *testing.T, now time.Time, log *pushLog, m domain.Monitor) checker.Result {
	t.Helper()
	return checker.NewPush(log, clock.NewFake(now)).Check(context.Background(), m)
}

// Entre as execuções previstas o silêncio é esperado. Com intervalo, uma
// rotina das 09:02 e das 18:02 viraria alarme toda tarde; com agenda, o
// monitor sabe que ninguém devia reportar antes das 18:02.
func TestPushScheduleToleratesSilenceBetweenRuns(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	log := &pushLog{at: time.Date(2026, 7, 28, 9, 3, 0, 0, sp), has: true}

	got := checkAt(t, time.Date(2026, 7, 28, 17, 59, 0, 0, sp), log, scheduledMonitor(t, "America/Sao_Paulo"))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// A folga existe para a rotina que atrasa uns minutos não gerar alarme.
func TestPushScheduleToleratesDelayWithinGrace(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	log := &pushLog{at: time.Date(2026, 7, 28, 9, 3, 0, 0, sp), has: true}

	got := checkAt(t, time.Date(2026, 7, 28, 18, 11, 0, 0, sp), log, scheduledMonitor(t, "America/Sao_Paulo"))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Falta o sinal de uma execução prevista: é a única situação em que o
// monitor com agenda cai. A mensagem nomeia a execução perdida, que é o
// que o operador vai procurar no log da rotina.
func TestPushScheduleMissedRunIsDown(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	log := &pushLog{at: time.Date(2026, 7, 28, 9, 3, 0, 0, sp), has: true}

	got := checkAt(t, time.Date(2026, 7, 28, 18, 13, 0, 0, sp), log, scheduledMonitor(t, "America/Sao_Paulo"))

	if got.Status != domain.StatusDown {
		t.Fatalf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusDown)
	}
	if !strings.Contains(got.Message, "18:02") {
		t.Errorf("Message = %q, want it to name the missed run at 18:02", got.Message)
	}
}

// O intervalo de 15h entre 18:02 e 09:02 não pode virar alarme de
// madrugada: o sinal das 18:05 cobre até a execução seguinte.
func TestPushScheduleCoversTheOvernightGap(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	log := &pushLog{at: time.Date(2026, 7, 28, 18, 5, 0, 0, sp), has: true}

	got := checkAt(t, time.Date(2026, 7, 29, 9, 5, 0, 0, sp), log, scheduledMonitor(t, "America/Sao_Paulo"))

	if got.Status != domain.StatusUp {
		t.Errorf("Status = %v (%s), want %v", got.Status, got.Message, domain.StatusUp)
	}
}

// Rotinas são agendadas no horário local, e o horário de verão desloca a
// execução em relação ao UTC. Com fuso fixo, o monitor esperaria o sinal
// uma hora depois do que a rotina realmente roda, e a falha chegaria
// atrasada justamente na semana da mudança.
func TestPushScheduleFollowsDaylightSavingTime(t *testing.T) {
	m := pushMonitor(t, time.Minute, map[string]any{
		"token": "segredo-de-16-chars", "schedule": "0 9 * * *",
		"timezone": "America/New_York", "grace_period_seconds": 600,
	})
	// Sábado 07/03/2026, 09:01 EST (14:01 UTC). No domingo começa o
	// horário de verão, e as 09:00 locais passam a ser 13:00 UTC.
	log := &pushLog{at: time.Date(2026, 3, 7, 14, 1, 0, 0, time.UTC), has: true}

	got := checkAt(t, time.Date(2026, 3, 8, 13, 15, 0, 0, time.UTC), log, m)

	if got.Status != domain.StatusDown {
		t.Errorf("Status = %v (%s), want %v: the 09:00 EDT run was missed",
			got.Status, got.Message, domain.StatusDown)
	}
}

// O dead-man's switch vale também com agenda: a contagem começa na
// primeira execução prevista depois da criação, não na própria criação.
// Um monitor criado às 08:00 para uma rotina das 09:02 não pode cair às
// 08:10, nem ficar esperando para sempre se a rotina nunca rodar.
func TestPushScheduleNeverSignalledCountsFromTheFirstExpectedRun(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	m := scheduledMonitor(t, "America/Sao_Paulo")
	m.CreatedAt = time.Date(2026, 7, 28, 8, 0, 0, 0, sp)

	before := checkAt(t, time.Date(2026, 7, 28, 9, 11, 0, 0, sp), &pushLog{has: false}, m)
	if before.Status != domain.StatusUnknown {
		t.Errorf("before the first deadline: Status = %v (%s), want %v",
			before.Status, before.Message, domain.StatusUnknown)
	}

	after := checkAt(t, time.Date(2026, 7, 28, 9, 13, 0, 0, sp), &pushLog{has: false}, m)
	if after.Status != domain.StatusDown {
		t.Errorf("after the first deadline: Status = %v (%s), want %v",
			after.Status, after.Message, domain.StatusDown)
	}
}

// A próxima execução esperada é o que a interface mostra no detalhe: é
// assim que um fuso errado aparece no cadastro, e não na primeira
// madrugada.
func TestPushNextExpectedRunFollowsTheSchedule(t *testing.T) {
	sp := mustLocation(t, "America/Sao_Paulo")
	cfg := json.RawMessage(`{"token":"segredo-de-16-chars","schedule":"2 9,18 * * *","timezone":"America/Sao_Paulo"}`)

	got, ok, err := checker.NextPushRun(cfg, time.Date(2026, 7, 28, 12, 0, 0, 0, sp))
	if err != nil || !ok {
		t.Fatalf("NextPushRun = (%v, %v, %v), want a scheduled run", got, ok, err)
	}
	if want := time.Date(2026, 7, 28, 18, 2, 0, 0, sp); !got.Equal(want) {
		t.Errorf("NextPushRun = %v, want %v", got, want)
	}
}

// Sem agenda não há execução prevista, só intervalo; inventar uma data
// faria a interface prometer algo que o monitor não vigia.
func TestPushNextExpectedRunWithoutScheduleIsAbsent(t *testing.T) {
	_, ok, err := checker.NextPushRun(json.RawMessage(`{"token":"segredo-de-16-chars"}`), pushNow)
	if err != nil || ok {
		t.Errorf("NextPushRun without schedule = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestPushValidateConfigAcceptsSchedule(t *testing.T) {
	cfg := json.RawMessage(`{"token":"um-token-suficientemente-longo","schedule":"2 9,18 * * 1-5","timezone":"America/Sao_Paulo"}`)

	if err := newPush(&pushLog{}).ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig returned unexpected error: %v", err)
	}
}

// Expressão errada precisa ser recusada no cadastro; aceitá-la deixaria o
// monitor sem saber quando esperar o sinal, e o erro só apareceria quando
// a rotina já tivesse falhado sem aviso.
func TestPushValidateConfigRejectsBadSchedules(t *testing.T) {
	cases := map[string]string{
		"lixo":                 `{"schedule":"toda hora","timezone":"UTC"}`,
		"seis campos":          `{"schedule":"0 2 9 * * *","timezone":"UTC"}`,
		"minuto fora da faixa": `{"schedule":"61 9 * * *","timezone":"UTC"}`,
		// 30 de fevereiro nunca chega: o monitor ficaria esperando para
		// sempre sem jamais cobrar o sinal.
		"nunca dispara": `{"schedule":"0 0 30 2 *","timezone":"UTC"}`,
		// Sem fuso explícito, "09:02" seria lido em UTC, e uma rotina de
		// São Paulo seria cobrada três horas antes de rodar.
		"sem fuso":         `{"schedule":"2 9 * * *"}`,
		"fuso inexistente": `{"schedule":"2 9 * * *","timezone":"America/Atlantida"}`,
		// O fuso vem embutido no campo próprio; aceitar também o prefixo
		// da biblioteca deixaria dois lugares decidindo a mesma coisa.
		"fuso no prefixo": `{"schedule":"CRON_TZ=UTC 2 9 * * *","timezone":"UTC"}`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			var cfg map[string]any
			if err := json.Unmarshal([]byte(extra), &cfg); err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			cfg["token"] = "um-token-suficientemente-longo"
			raw, _ := json.Marshal(cfg)

			if err := newPush(&pushLog{}).ValidateConfig(raw); err == nil {
				t.Errorf("ValidateConfig(%s) returned nil error, want an error", raw)
			}
		})
	}
}
