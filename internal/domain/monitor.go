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
	// MonitorRedis conversa com o Redis: PING e, opcionalmente, asserções
	// sobre comandos de leitura.
	MonitorRedis
	// MonitorPostgres conecta ao PostgreSQL e executa uma consulta.
	MonitorPostgres
	// MonitorMySQL conecta ao MySQL ou MariaDB e executa uma consulta.
	MonitorMySQL
)

var monitorTypeNames = map[MonitorType]string{
	MonitorHTTP:     "http",
	MonitorTCP:      "tcp",
	MonitorICMP:     "icmp",
	MonitorDNS:      "dns",
	MonitorTLS:      "tls",
	MonitorPush:     "push",
	MonitorRedis:    "redis",
	MonitorPostgres: "postgres",
	MonitorMySQL:    "mysql",
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

	// Config é a configuração específica do tipo de check, opaca para o
	// domínio e para o store — só o Checker correspondente a interpreta.
	//
	// JSON em vez de uma coluna por opção de cada tipo: sem isso a tabela
	// monitor ganharia um punhado de colunas nuláveis a cada checker novo,
	// que é como a do Uptime Kuma chegou a dezenas delas.
	Config json.RawMessage `json:"config,omitempty"`

	// ParentID prepara monitores hierárquicos: quando o pai cai, os filhos
	// não geram alerta próprio. O schema reserva o espaço desde já para
	// não exigir migração depois.
	ParentID *int64 `json:"parent_id,omitempty"`

	// GroupID é o grupo folha do monitor. Nulo é "Sem grupo", que é onde
	// toda instalação anterior aos grupos cai na migração.
	GroupID *int64 `json:"group_id,omitempty"`

	Enabled bool     `json:"enabled"`
	Tags    []string `json:"tags,omitempty"`

	// PushToken é o segredo do monitor push, copiado da configuração para
	// uma coluna própria, indexada e única.
	//
	// Existe para o /push achar o dono do token sem percorrer todos os
	// monitores — o endpoint não tem login, e cada requisição custaria uma
	// varredura — e para o banco impedir dois monitores com o mesmo token.
	// Quem o preenche é a API, a partir do checker, que é quem interpreta a
	// configuração; vazio fora do push.
	PushToken string `json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// RearmedAt é o instante da última mudança que recomeça a espera de
	// um monitor push: configuração, intervalo ou reativação. Começa na
	// criação.
	//
	// Separado de UpdatedAt porque aquele muda em qualquer edição, e
	// renomear não pode adiar um alarme legítimo. Sem ele, trocar a
	// agenda ou religar um monitor parado o derrubaria na hora, cobrando
	// uma janela que ninguém estava vigiando.
	RearmedAt time.Time `json:"rearmed_at"`
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
	if err := validateTags(m.Tags); err != nil {
		return err
	}
	return nil
}
