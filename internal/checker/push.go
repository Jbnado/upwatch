package checker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	// A base de fusos vai embarcada no binário: a imagem final não tem
	// tzdata, e sem ela todo monitor com agenda seria recusado no cadastro
	// por "fuso desconhecido".
	_ "time/tzdata"

	"github.com/robfig/cron/v3"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
)

// MinPushTokenLength é o tamanho mínimo do segredo do endpoint de push.
//
// Um token curto é adivinhável, e quem adivinha consegue manter um monitor
// artificialmente saudável enquanto o serviço real está parado — a falha
// mais perigosa possível num sistema de monitoramento.
const MinPushTokenLength = 16

// PushLog dá acesso ao último sinal recebido.
//
// Interface estreita: o checker não precisa do store inteiro, só de saber
// quando o serviço monitorado reportou pela última vez, e o que disse.
type PushLog interface {
	LastPush(ctx context.Context, monitorID int64) (domain.PushSignal, bool, error)
}

// PushConfig é a configuração de um monitor push.
type PushConfig struct {
	// Token autentica o endpoint que recebe o sinal.
	Token string `json:"token"`

	// GracePeriodSeconds é a folga antes de declarar silêncio: além do
	// intervalo, ou além da execução prevista quando há agenda. Zero usa
	// uma folga igual ao intervalo do monitor, de modo que um atraso
	// pontual não vire alerta.
	GracePeriodSeconds int `json:"grace_period_seconds,omitempty"`

	// Schedule é uma expressão cron de 5 campos com os horários em que o
	// sinal é esperado. Vazio mantém a expectativa por intervalo.
	//
	// Existe para rotinas de horário fixo: uma tarefa das 09:02 e das
	// 18:02 tem intervalos de 9h e de 15h, e nenhum intervalo único serve
	// — ou a falha da manhã só aparece no dia seguinte, ou toda noite vira
	// alarme falso.
	Schedule string `json:"schedule,omitempty"`

	// Timezone é o fuso IANA em que a agenda é lida. Obrigatório com
	// agenda: rotinas são agendadas no horário local, e o horário de
	// verão desloca a execução em relação ao UTC.
	Timezone string `json:"timezone,omitempty"`
}

// grace é a folga efetiva.
func (c PushConfig) grace(interval time.Duration) time.Duration {
	if c.GracePeriodSeconds > 0 {
		return time.Duration(c.GracePeriodSeconds) * time.Second
	}
	return interval
}

// pushPlan é a configuração já interpretada: sabe dizer até quando o
// próximo sinal pode chegar.
type pushPlan struct {
	cfg      PushConfig
	schedule cron.Schedule
	loc      *time.Location
}

// cronParser aceita só os 5 campos clássicos. Descritores como @daily e o
// campo de segundos ficam de fora para a expressão significar a mesma
// coisa aqui e no crontab de onde ela foi copiada.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func newPushPlan(cfg PushConfig) (pushPlan, error) {
	plan := pushPlan{cfg: cfg}
	if strings.TrimSpace(cfg.Schedule) == "" {
		return plan, nil
	}

	// O fuso tem campo próprio. Aceitar também o prefixo da biblioteca
	// deixaria dois lugares decidindo a mesma coisa, e eles divergiriam.
	if strings.Contains(cfg.Schedule, "TZ=") {
		return plan, errors.New("checker: informe o fuso em timezone, não na expressão")
	}
	if cfg.Timezone == "" {
		return plan, errors.New("checker: agenda exige timezone explícito (ex.: America/Sao_Paulo)")
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return plan, fmt.Errorf("checker: fuso %q desconhecido", cfg.Timezone)
	}
	sched, err := cronParser.Parse(cfg.Schedule)
	if err != nil {
		return plan, fmt.Errorf("checker: agenda inválida: %v", err)
	}

	plan.schedule, plan.loc = sched, loc
	return plan, nil
}

// scheduled informa se o sinal é esperado por agenda, e não por intervalo.
func (pl pushPlan) scheduled() bool { return pl.schedule != nil }

// nextRun é a primeira execução prevista depois de t, no fuso da agenda.
// Zero quando a expressão nunca dispara.
func (pl pushPlan) nextRun(t time.Time) time.Time {
	return pl.schedule.Next(t.In(pl.loc))
}

// deadline é o instante até o qual o sinal seguinte a after pode chegar.
//
// Por intervalo, é after mais intervalo e folga. Por agenda, é a próxima
// execução prevista depois de after mais a folga: um sinal das 18:05
// cobre a madrugada inteira até a execução das 09:02, e só a falta do
// sinal de uma execução prevista derruba o monitor. Devolve também a
// execução esperada, zero sem agenda.
func (pl pushPlan) deadline(after time.Time, interval time.Duration) (time.Time, time.Time) {
	grace := pl.cfg.grace(interval)
	if !pl.scheduled() {
		return after.Add(interval + grace), time.Time{}
	}
	run := pl.nextRun(after)
	if run.IsZero() {
		return time.Time{}, time.Time{}
	}
	return run.Add(grace), run
}

// Push verifica se o serviço monitorado reportou dentro da janela.
//
// Inverte o fluxo dos demais checkers: aqui quem bate é o serviço, o que
// permite vigiar tarefas agendadas e processos sem porta exposta.
type Push struct {
	log   PushLog
	clock clock.Clock
}

// NewPush cria o checker de push.
func NewPush(log PushLog, c clock.Clock) *Push {
	if c == nil {
		c = clock.Real()
	}
	return &Push{log: log, clock: c}
}

// Type identifica o tipo de monitor atendido.
func (p *Push) Type() domain.MonitorType { return domain.MonitorPush }

// ValidateConfig confere a configuração no cadastro.
func (p *Push) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parsePushConfig(raw)
	if err != nil {
		return err
	}
	if len(cfg.Token) < MinPushTokenLength {
		return fmt.Errorf(
			"checker: token de push precisa de pelo menos %d caracteres", MinPushTokenLength)
	}
	if cfg.GracePeriodSeconds < 0 {
		return fmt.Errorf("checker: grace_period_seconds não pode ser negativo")
	}

	plan, err := newPushPlan(cfg)
	if err != nil {
		return err
	}
	// Uma expressão válida que nunca dispara (30 de fevereiro) deixaria o
	// monitor esperando para sempre sem jamais cobrar o sinal.
	if plan.scheduled() && plan.nextRun(p.clock.Now()).IsZero() {
		return errors.New("checker: a agenda nunca dispara")
	}
	return nil
}

// NextPushRun devolve a próxima execução prevista depois de now, no fuso
// do monitor, e se há agenda.
//
// É o que a interface mostra no detalhe do monitor: um fuso ou uma
// expressão errados aparecem no cadastro, e não na primeira madrugada em
// que o aviso deveria ter chegado.
func NextPushRun(raw json.RawMessage, now time.Time) (time.Time, bool, error) {
	cfg, err := parsePushConfig(raw)
	if err != nil {
		return time.Time{}, false, err
	}
	plan, err := newPushPlan(cfg)
	if err != nil {
		return time.Time{}, false, err
	}
	if !plan.scheduled() {
		return time.Time{}, false, nil
	}
	run := plan.nextRun(now)
	return run, !run.IsZero(), nil
}

// Check compara o instante do último sinal com a janela tolerada.
func (p *Push) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parsePushConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}
	plan, err := newPushPlan(cfg)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	last, ok, err := p.log.LastPush(ctx, m.ID)
	if err != nil {
		return down("consultando o último sinal: %v", err)
	}
	now := p.clock.Now()
	if !ok {
		return neverSignalled(plan, m, now)
	}

	silence := now.Sub(last.At)
	res := Result{
		Meta: map[string]string{
			"last_push":          last.At.UTC().Format(time.RFC3339),
			"seconds_since_push": strconv.FormatInt(int64(silence.Seconds()), 10),
		},
	}

	// Silêncio vence o que o último sinal disse: um "up" antigo não pode
	// manter verde quem parou de reportar.
	limit, run := plan.deadline(last.At, m.Interval)
	if !limit.IsZero() && now.After(limit) {
		res.Status = domain.StatusDown
		res.Message = silenceMessage(plan, silence, run, limit, m.Interval)
		return res
	}

	return reported(res, last)
}

// silenceMessage descreve o silêncio. Com agenda, nomeia a execução
// perdida, que é o que o operador vai procurar no log da rotina.
func silenceMessage(plan pushPlan, silence time.Duration, run, limit time.Time, interval time.Duration) string {
	if !plan.scheduled() {
		return fmt.Sprintf("sem sinal há %s, acima da janela de %s",
			silence.Round(time.Second), interval+plan.cfg.grace(interval))
	}
	return fmt.Sprintf("sem sinal da execução prevista para %s (%s); a folga terminou às %s",
		run.Format("02/01 15:04"), plan.cfg.Timezone, limit.In(plan.loc).Format("15:04"))
}

// neverSignalled decide sobre o monitor que ainda não recebeu sinal algum.
//
// Durante a primeira janela depois da criação fica desconhecido: o serviço
// ainda não teve chance de reportar, e alertar seria acusar algo que nunca
// esteve no ar. Com agenda, a janela termina na primeira execução prevista
// depois da criação, mais a folga. Passada a janela vira queda, porque o
// push é um dead-man's switch — uma sonda que nunca subiu (script não
// agendado, token errado no agente) é exatamente a falha que ninguém
// perceberia sozinho.
//
// Sem data de criação não há de onde contar a janela, e o monitor fica
// desconhecido em vez de arriscar um alarme falso.
func neverSignalled(plan pushPlan, m domain.Monitor, now time.Time) Result {
	waiting := Result{Status: domain.StatusUnknown, Message: "aguardando o primeiro sinal"}
	if m.CreatedAt.IsZero() {
		return waiting
	}
	limit, _ := plan.deadline(m.CreatedAt, m.Interval)
	if limit.IsZero() || !now.After(limit) {
		return waiting
	}
	return Result{
		Status:  domain.StatusDown,
		Message: "nenhum sinal recebido desde a criação do monitor",
	}
}

// reported traduz o que o próprio serviço disse no sinal.
//
// Um "down" explícito vira uma falha como qualquer outra: passa pela
// confirmação de N verificações no motor de incidentes, sem atalho, para
// um soluço do script não abrir incidente sozinho.
//
// A latência só existe quando o serviço a mediu; zero continua
// significando "não medido", e o UpWatch não inventa número para um
// monitor que não faz ida e volta nenhuma.
func reported(res Result, sig domain.PushSignal) Result {
	res.Status = sig.Status
	res.Message = sig.Message
	res.LatencyMS = sig.LatencyMS

	if sig.Status == domain.StatusDown && res.Message == "" {
		res.Message = "o serviço reportou falha, sem informar a causa"
	}
	return res
}

// GeneratePushToken cria um segredo imprevisível para o endpoint.
func GeneratePushToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("checker: gerando token de push: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func parsePushConfig(raw json.RawMessage) (PushConfig, error) {
	var cfg PushConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração de push inválida: %w", err)
	}
	return cfg, nil
}
