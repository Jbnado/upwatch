package checker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Jbnado/upwatch/internal/clock"
	"github.com/Jbnado/upwatch/internal/domain"
)

// MinPushTokenLength é o tamanho mínimo do segredo do endpoint de push.
//
// Um token curto é adivinhável, e quem adivinha consegue manter um monitor
// artificialmente saudável enquanto o serviço real está parado — a falha
// mais perigosa possível num sistema de monitoramento.
const MinPushTokenLength = 16

// PushLog dá acesso ao instante do último sinal recebido.
//
// Interface estreita: o checker não precisa do store inteiro, só de saber
// quando o serviço monitorado reportou pela última vez.
type PushLog interface {
	LastPush(ctx context.Context, monitorID int64) (time.Time, bool, error)
}

// PushConfig é a configuração de um monitor push.
type PushConfig struct {
	// Token autentica o endpoint que recebe o sinal.
	Token string `json:"token"`

	// GracePeriodSeconds é a folga além do intervalo antes de declarar
	// silêncio. Zero usa uma folga igual ao próprio intervalo, de modo que
	// um atraso pontual não vire alerta.
	GracePeriodSeconds int `json:"grace_period_seconds,omitempty"`
}

// window é quanto tempo de silêncio é tolerado.
func (c PushConfig) window(interval time.Duration) time.Duration {
	if c.GracePeriodSeconds > 0 {
		return interval + time.Duration(c.GracePeriodSeconds)*time.Second
	}
	return interval * 2
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
	return nil
}

// Check compara o instante do último sinal com a janela tolerada.
func (p *Push) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parsePushConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	last, ok, err := p.log.LastPush(ctx, m.ID)
	if err != nil {
		return down("consultando o último sinal: %v", err)
	}
	if !ok {
		// Monitor recém-criado ainda não teve chance de reportar; marcar
		// como fora do ar dispararia alerta por algo que nunca esteve no ar.
		return Result{
			Status:  domain.StatusUnknown,
			Message: "aguardando o primeiro sinal",
		}
	}

	now := p.clock.Now()
	silence := now.Sub(last)
	window := cfg.window(m.Interval)

	// Latência fica zerada de propósito: um monitor push não mede tempo de
	// resposta de nada, e preenchê-la poluiria os percentis com um número
	// sem significado.
	res := Result{
		Meta: map[string]string{
			"last_push":          last.UTC().Format(time.RFC3339),
			"seconds_since_push": strconv.FormatInt(int64(silence.Seconds()), 10),
		},
	}

	if silence > window {
		res.Status = domain.StatusDown
		res.Message = fmt.Sprintf("sem sinal há %s, acima da janela de %s",
			silence.Round(time.Second), window)
		return res
	}

	res.Status = domain.StatusUp
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
