package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/Jbnado/upwatch/internal/domain"
)

// DefaultPingCount é quantos pacotes cada verificação envia.
//
// Três em vez de um: um pacote isolado transforma qualquer perda pontual
// num alerta, enquanto três permitem distinguir queda de perda parcial.
const DefaultPingCount = 3

// PingStats resume o resultado de uma rodada de echo requests.
type PingStats struct {
	Sent     int
	Received int
	AvgRTT   time.Duration
	MinRTT   time.Duration
	MaxRTT   time.Duration
}

// PacketLoss é a perda em porcentagem.
func (s PingStats) PacketLoss() float64 {
	if s.Sent == 0 {
		return 0
	}
	return float64(s.Sent-s.Received) / float64(s.Sent) * 100
}

// Pinger envia echo requests.
//
// Existe como interface porque o envio depende de socket privilegiado e
// da rede real: separá-lo permite verificar perda parcial, perda total e
// falta de permissão, cenários que não se provoca de forma confiável
// contra a rede de verdade.
type Pinger interface {
	Ping(ctx context.Context, host string, count int, timeout time.Duration) (PingStats, error)
}

// ICMPConfig é a configuração de um monitor de ping.
type ICMPConfig struct {
	// Count é quantos pacotes enviar. Zero usa o padrão.
	Count int `json:"count,omitempty"`

	// MaxPacketLoss é a perda tolerada, em porcentagem. Acima disso o
	// monitor fica degradado; redes onde alguma perda é normal precisam
	// tolerá-la para o monitor não virar fonte de ruído.
	MaxPacketLoss float64 `json:"max_packet_loss,omitempty"`

	// Privileged força socket cru em vez de UDP. Necessário onde o sistema
	// não oferece o modo não privilegiado; o Windows é sempre assim, e lá
	// a opção é ligada automaticamente.
	Privileged bool `json:"privileged,omitempty"`
}

// privileged decide o modo do socket.
//
// O Windows não implementa ICMP sobre UDP, então insistir no modo não
// privilegiado lá falharia com "protocolo não suportado" — um erro que
// pareceria problema de rede sem ter nada a ver com ela.
func (c ICMPConfig) privileged() bool {
	return c.Privileged || runtime.GOOS == "windows"
}

func (c ICMPConfig) count() int {
	if c.Count <= 0 {
		return DefaultPingCount
	}
	return c.Count
}

// ICMP verifica alcançabilidade por echo request.
type ICMP struct {
	pinger Pinger
}

// NewICMP cria o checker de ping usando a rede real.
func NewICMP() *ICMP {
	return &ICMP{pinger: netPinger{}}
}

// NewICMPWith cria o checker com um Pinger próprio.
func NewICMPWith(p Pinger) *ICMP {
	return &ICMP{pinger: p}
}

// Type identifica o tipo de monitor atendido.
func (c *ICMP) Type() domain.MonitorType { return domain.MonitorICMP }

// ValidateConfig confere a configuração no cadastro.
func (c *ICMP) ValidateConfig(raw json.RawMessage) error {
	cfg, err := parseICMPConfig(raw)
	if err != nil {
		return err
	}
	if raw != nil && cfg.Count < 0 {
		return fmt.Errorf("checker: count não pode ser negativo")
	}
	// Zero explícito é rejeitado: enviar nenhum pacote nunca é a intenção,
	// então é mais provável ser engano que uso deliberado do padrão.
	if bytesHaveKey(raw, "count") && cfg.Count <= 0 {
		return fmt.Errorf("checker: count precisa ser pelo menos 1")
	}
	if cfg.MaxPacketLoss < 0 || cfg.MaxPacketLoss > 100 {
		return fmt.Errorf("checker: max_packet_loss precisa estar entre 0 e 100")
	}
	return nil
}

// Check envia os pacotes e classifica o resultado.
func (c *ICMP) Check(ctx context.Context, m domain.Monitor) Result {
	cfg, err := parseICMPConfig(m.Config)
	if err != nil {
		return down("configuração inválida: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()

	// O modo do socket vem da configuração do monitor, então o pinger real
	// é montado por verificação; ele não guarda estado entre chamadas.
	pinger := c.pinger
	if def, ok := pinger.(netPinger); ok {
		def.privileged = cfg.privileged()
		pinger = def
	}

	stats, err := pinger.Ping(ctx, strings.TrimSpace(m.Target), cfg.count(), m.Timeout)
	if err != nil {
		return down("%s", explainPingError(err))
	}

	loss := stats.PacketLoss()
	res := Result{
		LatencyMS: stats.AvgRTT.Milliseconds(),
		Meta: map[string]string{
			"packets_sent":     strconv.Itoa(stats.Sent),
			"packets_received": strconv.Itoa(stats.Received),
			"packet_loss":      strconv.FormatFloat(loss, 'f', -1, 64),
			"rtt_min_ms":       strconv.FormatInt(stats.MinRTT.Milliseconds(), 10),
			"rtt_max_ms":       strconv.FormatInt(stats.MaxRTT.Milliseconds(), 10),
		},
	}

	switch {
	case stats.Received == 0:
		res.Status = domain.StatusDown
		res.LatencyMS = 0
		res.Message = fmt.Sprintf("nenhuma resposta a %d pacotes", stats.Sent)
	case loss > cfg.MaxPacketLoss:
		// Perda parcial não é queda, mas tratá-la como saúde esconderia a
		// degradação até ela virar indisponibilidade.
		res.Status = domain.StatusDegraded
		res.Message = fmt.Sprintf("perda de %.0f%% dos pacotes", loss)
	default:
		res.Status = domain.StatusUp
	}
	return res
}

// explainPingError traduz falha de permissão numa instrução acionável.
//
// Sem isso o operador procura problema na rede quando o problema é o
// contêiner não ter a capacidade necessária.
func explainPingError(err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not permitted") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "socket: operation not permitted") {
		return fmt.Sprintf(
			"sem privilégio para enviar ICMP (%v); conceda CAP_NET_RAW ao contêiner "+
				"ou ajuste net.ipv4.ping_group_range no host", err)
	}
	return err.Error()
}

// netPinger envia pacotes de verdade.
type netPinger struct {
	// privileged força socket cru. Definido pelo checker a partir da
	// configuração do monitor e da plataforma.
	privileged bool
}

func (n netPinger) Ping(ctx context.Context, host string, count int, timeout time.Duration) (PingStats, error) {
	return runPing(ctx, host, count, timeout, n.privileged)
}

func runPing(ctx context.Context, host string, count int, timeout time.Duration, privileged bool) (PingStats, error) {
	p, err := probing.NewPinger(host)
	if err != nil {
		return PingStats{}, err
	}
	p.Count = count
	p.Timeout = timeout
	// No Linux o modo não privilegiado usa socket UDP e dispensa
	// CAP_NET_RAW, que é o caminho preferido no contêiner. O Windows não
	// oferece esse modo: lá o socket cru é a única opção, e insistir no
	// não privilegiado falha com "protocolo não suportado".
	p.SetPrivileged(privileged)

	if err := p.RunWithContext(ctx); err != nil {
		return PingStats{}, err
	}

	s := p.Statistics()
	return PingStats{
		Sent:     s.PacketsSent,
		Received: s.PacketsRecv,
		AvgRTT:   s.AvgRtt,
		MinRTT:   s.MinRtt,
		MaxRTT:   s.MaxRtt,
	}, nil
}

// bytesHaveKey informa se o JSON traz a chave explicitamente, para
// distinguir "não informado" de "informado como zero".
func bytesHaveKey(raw json.RawMessage, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func parseICMPConfig(raw json.RawMessage) (ICMPConfig, error) {
	var cfg ICMPConfig
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("checker: configuração ICMP inválida: %w", err)
	}
	return cfg, nil
}
