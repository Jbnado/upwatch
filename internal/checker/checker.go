// Package checker define como um alvo é verificado.
//
// Cada tipo de monitor — HTTP, TCP, ICMP, DNS, TLS, push — implementa a
// mesma interface e é resolvido pelo Registry. Adicionar um tipo novo não
// exige mudança no agendador, no store nem no domínio: a configuração
// específica viaja como JSON opaco no monitor.
package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
)

// Result é o desfecho de um check.
type Result struct {
	Status    domain.Status
	LatencyMS int64
	// Message descreve a causa quando o check não passou.
	Message string
	// Meta traz dados auxiliares do tipo de check — código de status HTTP,
	// dias até o certificado expirar, IP resolvido.
	Meta map[string]string
}

// Checker verifica um tipo de alvo.
//
// Check nunca devolve erro: uma falha de rede é um resultado legítimo
// (Down com mensagem), não uma exceção. Isso mantém o agendador simples,
// já que todo check produz exatamente uma batida.
type Checker interface {
	Type() domain.MonitorType
	Check(ctx context.Context, m domain.Monitor) Result
	// ValidateConfig confere a configuração específica do tipo antes de o
	// monitor ser gravado, para o erro aparecer no cadastro e não só na
	// primeira execução.
	ValidateConfig(cfg json.RawMessage) error
}

// Registry resolve o Checker de cada monitor e aplica as regras comuns a
// todos os tipos.
type Registry struct {
	byType map[domain.MonitorType]Checker
}

// NewRegistry monta o registry.
//
// Dois checkers para o mesmo tipo é erro de montagem: um sobrescreveria o
// outro em silêncio e o monitor passaria a ser verificado de outra forma
// sem que nada indicasse.
func NewRegistry(cs ...Checker) (*Registry, error) {
	byType := make(map[domain.MonitorType]Checker, len(cs))
	for _, c := range cs {
		if _, dup := byType[c.Type()]; dup {
			return nil, fmt.Errorf("checker: mais de um checker registrado para o tipo %s", c.Type())
		}
		byType[c.Type()] = c
	}
	return &Registry{byType: byType}, nil
}

// Get devolve o checker do tipo pedido.
func (r *Registry) Get(t domain.MonitorType) (Checker, error) {
	c, ok := r.byType[t]
	if !ok {
		return nil, fmt.Errorf("checker: nenhum checker registrado para o tipo %s", t)
	}
	return c, nil
}

// Types lista os tipos atendidos.
func (r *Registry) Types() []domain.MonitorType {
	out := make([]domain.MonitorType, 0, len(r.byType))
	for t := range r.byType {
		out = append(out, t)
	}
	return out
}

// Validate confere se o monitor pode ser verificado por algum checker e se
// sua configuração é aceita por ele.
func (r *Registry) Validate(m domain.Monitor) error {
	c, err := r.Get(m.Type)
	if err != nil {
		return err
	}
	return c.ValidateConfig(m.Config)
}

// Check executa a verificação e aplica as regras válidas para todo tipo.
//
// Nunca devolve pânico nem erro: o agendador precisa receber uma batida
// para cada execução, aconteça o que acontecer.
func (r *Registry) Check(ctx context.Context, m domain.Monitor) Result {
	c, err := r.Get(m.Type)
	if err != nil {
		return Result{Status: domain.StatusDown, Message: err.Error()}
	}

	res := safeCheck(ctx, c, m)
	return applyDegradedLatency(res, m)
}

// safeCheck isola um pânico do checker.
//
// Sem isto um defeito em um único tipo de check derrubaria o processo e
// pararia o monitoramento de todos os alvos.
func safeCheck(ctx context.Context, c Checker, m domain.Monitor) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			res = Result{
				Status:  domain.StatusDown,
				Message: fmt.Sprintf("pânico no checker %s: %v", m.Type, p),
			}
		}
	}()
	return c.Check(ctx, m)
}

// applyDegradedLatency rebaixa para Degraded uma resposta lenta demais.
//
// A regra vive aqui, e não em cada checker, para que todo tipo a receba e
// se comporte igual. Limiar zero desliga a detecção — do contrário todo
// monitor sem configuração viraria degradado.
func applyDegradedLatency(res Result, m domain.Monitor) Result {
	if m.DegradedLatency <= 0 || res.Status != domain.StatusUp {
		return res
	}
	if time.Duration(res.LatencyMS)*time.Millisecond > m.DegradedLatency {
		res.Status = domain.StatusDegraded
		if res.Message == "" {
			res.Message = fmt.Sprintf("resposta em %dms, acima do limiar de %s",
				res.LatencyMS, m.DegradedLatency)
		}
	}
	return res
}
