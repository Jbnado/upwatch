package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MonitorState é o que a máquina de confirmação precisa lembrar entre
// verificações.
//
// Vive no domínio, e não no pacote que a opera, para o store poder
// persisti-la sem inverter as camadas. Persistir importa: sem isso um
// reinício zeraria a contagem, e um alvo prestes a ser declarado fora do
// ar voltaria à estaca zero — atrasando a detecção em várias janelas.
type MonitorState struct {
	// Status é o estado confirmado, o que vale para alerta e interface.
	Status Status `json:"status"`
	// Candidate é o estado tentando se confirmar.
	Candidate Status `json:"candidate"`
	// Consecutive é quantas observações seguidas o candidato acumulou.
	Consecutive int `json:"consecutive"`
	// Since é desde quando o estado confirmado vale.
	Since time.Time `json:"since"`
}

// ChangeKind identifica uma mudança confirmada de estado.
type ChangeKind uint8

const (
	// ChangeDown é a confirmação de que o alvo está fora do ar.
	ChangeDown ChangeKind = iota + 1
	// ChangeUp é a volta confirmada.
	ChangeUp
	// ChangeDegraded é a confirmação de que o alvo responde, porém fora do
	// esperado.
	ChangeDegraded
)

var changeNames = map[ChangeKind]string{
	ChangeDown:     "down",
	ChangeUp:       "up",
	ChangeDegraded: "degraded",
}

func (k ChangeKind) String() string {
	if nome, ok := changeNames[k]; ok {
		return nome
	}
	return "unknown"
}

// StateChange é uma transição confirmada de estado de um monitor.
//
// Vive no domínio porque é o vocabulário comum entre quem decide a
// transição e quem a comunica; deixá-la num dos dois obrigaria o outro a
// importá-lo, e os dois a se conhecerem.
type StateChange struct {
	Kind ChangeKind
	// From é o estado anterior confirmado.
	From Status
	// To é o estado que passou a valer.
	To Status
	// At é o instante da observação que confirmou a mudança.
	At time.Time
	// Duration é quanto durou o estado anterior. Responde à primeira
	// pergunta depois de "voltou?", sem obrigar ninguém a subtrair dois
	// horários de cabeça.
	Duration time.Duration
}

// Resolves informa se a mudança encerra uma indisponibilidade.
func (c StateChange) Resolves() bool {
	return c.From == StatusDown && c.To != StatusDown
}

// Incident é uma janela de indisponibilidade confirmada.
type Incident struct {
	ID        int64     `json:"id"`
	MonitorID int64     `json:"monitor_id"`
	StartedAt time.Time `json:"started_at"`
	// ResolvedAt nulo significa que ainda está em curso.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// Cause é a falha observada no momento da confirmação.
	Cause string `json:"cause,omitempty"`
}

// Open informa se o incidente ainda não terminou.
func (i Incident) Open() bool { return i.ResolvedAt == nil }

// Duration é quanto durou, ou quanto já dura.
func (i Incident) Duration(now time.Time) time.Duration {
	if i.ResolvedAt != nil {
		return i.ResolvedAt.Sub(i.StartedAt)
	}
	return now.Sub(i.StartedAt)
}

// maxChannelNameLength limita o nome de um canal.
const maxChannelNameLength = 80

// Channel é um destino de aviso.
type Channel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Type identifica o notificador: webhook, discord, slack.
	Type string `json:"type"`
	// Config é a configuração específica do tipo, opaca para o domínio.
	//
	// Nunca sai pela API: costuma conter a URL secreta do webhook, que é
	// a própria credencial de quem pode publicar no canal.
	Config json.RawMessage `json:"-"`

	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes do canal.
func (c Channel) Validate() error {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return invalid("name", "não pode ser vazio")
	}
	if len(name) > maxChannelNameLength {
		return invalid("name", fmt.Sprintf("não pode passar de %d caracteres", maxChannelNameLength))
	}
	if strings.TrimSpace(c.Type) == "" {
		return invalid("type", "não pode ser vazio")
	}
	return nil
}

// MarshalJSON serializa o canal sem a configuração.
//
// A URL de um webhook é a credencial de quem pode publicar naquele canal;
// devolvê-la numa listagem a exporia a qualquer pessoa com acesso de
// leitura à interface.
func (c Channel) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID        int64     `json:"id"`
		Name      string    `json:"name"`
		Type      string    `json:"type"`
		Enabled   bool      `json:"enabled"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	return json.Marshal(alias{
		ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	})
}
