package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MinInterval é o menor intervalo aceito entre dois checks do mesmo monitor.
// Serve de guarda contra configuração acidental que transformaria o UpWatch
// numa fonte de carga sobre o alvo.
const MinInterval = 5 * time.Second

// MonitorType identifica qual Checker atende o monitor.
type MonitorType uint8

const (
	// MonitorHTTP faz requisição HTTP(S) e valida a resposta.
	MonitorHTTP MonitorType = iota + 1
	// MonitorTCP abre conexão TCP e verifica se a porta aceita.
	MonitorTCP
	// MonitorICMP envia echo request.
	MonitorICMP
	// MonitorDNS resolve um nome e valida o registro.
	MonitorDNS
	// MonitorTLS inspeciona validade e expiração do certificado.
	MonitorTLS
	// MonitorPush inverte o fluxo: o serviço monitorado bate no UpWatch.
	MonitorPush
)

var monitorTypeNames = map[MonitorType]string{
	MonitorHTTP: "http",
	MonitorTCP:  "tcp",
	MonitorICMP: "icmp",
	MonitorDNS:  "dns",
	MonitorTLS:  "tls",
	MonitorPush: "push",
}

// String devolve o nome canônico do tipo.
func (t MonitorType) String() string {
	if name, ok := monitorTypeNames[t]; ok {
		return name
	}
	return "unknown"
}

// Valid informa se o tipo pertence ao conjunto conhecido.
func (t MonitorType) Valid() bool {
	_, ok := monitorTypeNames[t]
	return ok
}

// ParseMonitorType converte o nome canônico de volta em MonitorType.
func ParseMonitorType(name string) (MonitorType, error) {
	for typ, n := range monitorTypeNames {
		if n == name {
			return typ, nil
		}
	}
	return 0, fmt.Errorf("domain: tipo de monitor inválido %q", name)
}

// MarshalJSON implementa json.Marshaler.
func (t MonitorType) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// UnmarshalJSON implementa json.Unmarshaler.
func (t *MonitorType) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	parsed, err := ParseMonitorType(name)
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// ValidationError aponta o campo que reprovou, para a API conseguir
// devolver 400 dizendo exatamente o que corrigir.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

func invalid(field, msg string) *ValidationError {
	return &ValidationError{Field: field, Msg: msg}
}

// Monitor é a definição de um alvo monitorado.
type Monitor struct {
	ID   int64       `json:"id"`
	Name string      `json:"name"`
	Type MonitorType `json:"type"`

	// Target é a URL, host:porta ou hostname, conforme o tipo.
	// Vazio para MonitorPush, onde quem bate é o serviço monitorado.
	Target string `json:"target"`

	Interval time.Duration `json:"interval"`
	Timeout  time.Duration `json:"timeout"`

	// ConfirmationThreshold é quantas falhas consecutivas são necessárias
	// antes de declarar o monitor fora do ar. Evita alerta a cada soluço
	// de rede.
	ConfirmationThreshold int `json:"confirmation_threshold"`

	// DegradedLatency marca o monitor como degradado quando a resposta
	// demora mais que isto. Zero desliga a detecção.
	DegradedLatency time.Duration `json:"degraded_latency"`

	// ParentID prepara monitores hierárquicos: quando o pai cai, os filhos
	// não geram alerta próprio. O schema reserva o espaço desde já para
	// não exigir migração depois.
	ParentID *int64 `json:"parent_id,omitempty"`

	Enabled bool     `json:"enabled"`
	Tags    []string `json:"tags,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes do monitor e devolve *ValidationError
// apontando o primeiro campo inválido.
func (m Monitor) Validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return invalid("name", "não pode ser vazio")
	}
	if !m.Type.Valid() {
		return invalid("type", "tipo de monitor desconhecido")
	}
	if m.Type != MonitorPush && strings.TrimSpace(m.Target) == "" {
		return invalid("target", "não pode ser vazio")
	}
	if m.Interval < MinInterval {
		return invalid("interval", fmt.Sprintf("deve ser no mínimo %s", MinInterval))
	}
	if m.Timeout <= 0 {
		return invalid("timeout", "deve ser maior que zero")
	}
	if m.Timeout >= m.Interval {
		return invalid("timeout", "deve ser menor que o intervalo, senão os checks se acumulam")
	}
	if m.ConfirmationThreshold < 1 {
		return invalid("confirmation_threshold", "deve ser no mínimo 1")
	}
	if m.DegradedLatency < 0 {
		return invalid("degraded_latency", "não pode ser negativo")
	}
	if m.ParentID != nil && *m.ParentID == m.ID && m.ID != 0 {
		return invalid("parent_id", "um monitor não pode ser pai de si mesmo")
	}
	return nil
}
