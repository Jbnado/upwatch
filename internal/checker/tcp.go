package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// TCPConfig é a configuração de um monitor TCP. Hoje vazia: abrir a
// conexão já é a verificação inteira. Existe para o formato do monitor não
// mudar quando surgirem opções.
type TCPConfig struct{}

// TCP verifica se uma porta aceita conexão.
type TCP struct {
	dialer *net.Dialer
}

// NewTCP cria o checker TCP.
func NewTCP() *TCP {
	return &TCP{dialer: &net.Dialer{}}
}

// Type identifica o tipo de monitor atendido.
func (c *TCP) Type() domain.MonitorType { return domain.MonitorTCP }

// ValidateConfig confere a configuração no cadastro.
func (c *TCP) ValidateConfig(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var cfg TCPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("checker: configuração TCP inválida: %w", err)
	}
	return nil
}

// Check abre uma conexão e a encerra em seguida.
//
// Fechar imediatamente é deliberado: o objetivo é saber se a porta aceita,
// não consumir um slot de conexão do serviço monitorado a cada sondagem.
func (c *TCP) Check(ctx context.Context, m domain.Monitor) Result {
	host, port, err := net.SplitHostPort(m.Target)
	if err != nil {
		// Alvo malformado é erro de configuração, não indisponibilidade.
		// Dizer isso evita o operador caçar problema de rede que não existe.
		return down("alvo precisa estar no formato host:porta, recebido %q", m.Target)
	}
	if port == "" {
		return down("alvo %q não informa a porta", m.Target)
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	start := time.Now()
	conn, err := c.dialer.DialContext(ctx, "tcp", m.Target)
	latency := time.Since(start).Milliseconds()

	res := Result{
		LatencyMS: latency,
		Meta:      map[string]string{"host": host, "port": port},
	}

	if err != nil {
		res.Status = domain.StatusDown
		res.Message = err.Error()
		res.LatencyMS = 0
		return res
	}
	_ = conn.Close()

	res.Status = domain.StatusUp
	return res
}
