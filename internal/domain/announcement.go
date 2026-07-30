package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// O relato público de um incidente.
//
// As páginas de estado que servem de referência têm duas camadas que se
// completam sem se misturar. As barras de noventa dias a máquina
// preenche, a partir das verificações. O relato — "estamos investigando
// erros elevados na API", depois "identificamos", depois "resolvido" — é
// escrito por uma pessoa.
//
// Ficam separados de propósito. A causa que a sonda detecta é literal e
// interna: "dial tcp 10.0.3.7:5432: connect: connection refused". Publicá-la
// entregaria endereço, porta e tecnologia de um serviço que ninguém de
// fora deveria enxergar. Então o Incident automático alimenta as barras,
// e o Announcement carrega o que uma pessoa decidiu contar.
//
// A consequência de desenho: uma instalação recém-subida mostra as barras
// e "nenhum incidente relatado". É o comportamento correto — o silêncio é
// preferível a um vazamento automático.

const (
	// maxAnnouncementTitleLength limita o título do relato.
	maxAnnouncementTitleLength = 160

	// maxAnnouncementBodyLength limita o corpo de uma atualização. Generoso:
	// um relato de post-mortem breve cabe, um despejo de log não.
	maxAnnouncementBodyLength = 4000
)

// IncidentImpact classifica a gravidade para quem lê de fora.
//
// Quatro níveis, como nas páginas de referência: quem acompanha já
// reconhece o vocabulário, e reinventá-lo só criaria tradução mental.
type IncidentImpact uint8

const (
	// ImpactNone é aviso informativo, sem degradação.
	ImpactNone IncidentImpact = iota
	// ImpactMinor é degradação parcial que a maioria não percebe.
	ImpactMinor
	// ImpactMajor é funcionalidade importante indisponível.
	ImpactMajor
	// ImpactCritical é serviço fora do ar.
	ImpactCritical
)

var impactNames = map[IncidentImpact]string{
	ImpactNone:     "none",
	ImpactMinor:    "minor",
	ImpactMajor:    "major",
	ImpactCritical: "critical",
}

// String devolve o nome canônico do impacto.
func (i IncidentImpact) String() string {
	if nome, ok := impactNames[i]; ok {
		return nome
	}
	return "none"
}

// Valid informa se o impacto pertence ao conjunto conhecido.
func (i IncidentImpact) Valid() bool {
	_, ok := impactNames[i]
	return ok
}

// ParseIncidentImpact converte o nome canônico de volta.
func ParseIncidentImpact(name string) (IncidentImpact, error) {
	for impacto, n := range impactNames {
		if n == name {
			return impacto, nil
		}
	}
	return ImpactNone, fmt.Errorf("domain: impacto inválido %q", name)
}

// MarshalJSON implementa json.Marshaler.
func (i IncidentImpact) MarshalJSON() ([]byte, error) {
	return json.Marshal(i.String())
}

// UnmarshalJSON implementa json.Unmarshaler.
func (i *IncidentImpact) UnmarshalJSON(data []byte) error {
	var nome string
	if err := json.Unmarshal(data, &nome); err != nil {
		return err
	}
	parsed, err := ParseIncidentImpact(nome)
	if err != nil {
		return err
	}
	*i = parsed
	return nil
}

// IncidentPhase é o estágio da comunicação.
//
// Investigando, identificado, monitorando, resolvido. A sequência é o que
// responde à pergunta que quem espera realmente faz — não "o que
// quebrou", mas "vocês já sabem, e falta muito?".
type IncidentPhase uint8

const (
	// PhaseInvestigating é o reconhecimento: sabemos que há algo errado.
	PhaseInvestigating IncidentPhase = iota + 1
	// PhaseIdentified é a causa encontrada, correção em andamento.
	PhaseIdentified
	// PhaseMonitoring é a correção aplicada, observando se sustenta.
	PhaseMonitoring
	// PhaseResolved encerra o relato.
	PhaseResolved
)

var phaseNames = map[IncidentPhase]string{
	PhaseInvestigating: "investigating",
	PhaseIdentified:    "identified",
	PhaseMonitoring:    "monitoring",
	PhaseResolved:      "resolved",
}

// String devolve o nome canônico da fase.
func (p IncidentPhase) String() string {
	if nome, ok := phaseNames[p]; ok {
		return nome
	}
	return "unknown"
}

// Valid informa se a fase pertence ao conjunto conhecido.
func (p IncidentPhase) Valid() bool {
	_, ok := phaseNames[p]
	return ok
}

// ParseIncidentPhase converte o nome canônico de volta.
func ParseIncidentPhase(name string) (IncidentPhase, error) {
	for fase, n := range phaseNames {
		if n == name {
			return fase, nil
		}
	}
	return 0, fmt.Errorf("domain: fase de incidente inválida %q", name)
}

// MarshalJSON implementa json.Marshaler.
func (p IncidentPhase) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

// UnmarshalJSON implementa json.Unmarshaler.
func (p *IncidentPhase) UnmarshalJSON(data []byte) error {
	var nome string
	if err := json.Unmarshal(data, &nome); err != nil {
		return err
	}
	parsed, err := ParseIncidentPhase(nome)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// Announcement é um relato publicado.
//
// Não pertence a uma página. Um alvo pode estar em várias — a página do
// produto e a de um cliente específico — e um relato por página obrigaria
// a escrever a mesma queda duas vezes, com as duas versões divergindo na
// terceira atualização. Aqui ele se escreve uma vez e aparece onde é
// relevante, pela regra de ShowsOn.
type Announcement struct {
	ID int64 `json:"id"`

	Title  string         `json:"title"`
	Impact IncidentImpact `json:"impact"`
	Phase  IncidentPhase  `json:"phase"`

	// IncidentID liga o relato ao incidente detectado, quando houve um.
	// Nulo permite anunciar o que nenhuma sonda vê — uma degradação
	// relatada por cliente, ou uma janela de manutenção.
	IncidentID *int64 `json:"incident_id,omitempty"`

	// Components são os monitores afetados. Vazio significa "toda a
	// plataforma", e é o que se quer para um aviso amplo.
	Components []int64 `json:"components,omitempty"`

	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes do relato.
func (a Announcement) Validate() error {
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return invalid("title", "não pode ser vazio")
	}
	if len(title) > maxAnnouncementTitleLength {
		return invalid("title", fmt.Sprintf("não pode passar de %d caracteres", maxAnnouncementTitleLength))
	}
	if !a.Impact.Valid() {
		return invalid("impact", "impacto desconhecido")
	}
	if !a.Phase.Valid() {
		return invalid("phase", "fase desconhecida")
	}
	return nil
}

// Resolved informa se o relato foi encerrado.
//
// Derivado da fase, não um campo próprio: dois lugares guardando o mesmo
// fato divergem, e o que a página mostra passaria a depender de qual dos
// dois foi consultado.
func (a Announcement) Resolved() bool { return a.Phase == PhaseResolved }

// ShowsOn informa se o relato aparece numa página, dados os monitores
// que a página publica.
//
// Sem componente algum o relato é da plataforma inteira e aparece em
// todas as páginas — é o que se quer de "migração de banco hoje à
// noite". Com componentes, aparece onde ao menos um deles é publicado:
// escrever a mesma queda uma vez por página é o caminho para versões
// divergentes do mesmo fato.
func (a Announcement) ShowsOn(pageMonitorIDs []int64) bool {
	if len(a.Components) == 0 {
		return true
	}

	publicado := make(map[int64]struct{}, len(pageMonitorIDs))
	for _, id := range pageMonitorIDs {
		publicado[id] = struct{}{}
	}
	for _, id := range a.Components {
		if _, ok := publicado[id]; ok {
			return true
		}
	}
	return false
}

// AnnouncementUpdate é uma entrada da linha do tempo.
type AnnouncementUpdate struct {
	ID             int64         `json:"id"`
	AnnouncementID int64         `json:"announcement_id"`
	Phase          IncidentPhase `json:"phase"`
	Body           string        `json:"body"`
	PublishedAt    time.Time     `json:"published_at"`
}

// Validate confere as invariantes da atualização.
func (u AnnouncementUpdate) Validate() error {
	body := strings.TrimSpace(u.Body)
	if body == "" {
		return invalid("body", "não pode ser vazio")
	}
	if len(body) > maxAnnouncementBodyLength {
		return invalid("body", fmt.Sprintf("não pode passar de %d caracteres", maxAnnouncementBodyLength))
	}
	if !u.Phase.Valid() {
		return invalid("phase", "fase desconhecida")
	}
	return nil
}

// PublicAnnouncement é o relato como a página o entrega.
//
// Components vem como rótulo, não como identificador: o número não diz
// nada a quem lê, e revelaria quantos monitores a instalação tem.
type PublicAnnouncement struct {
	Title  string         `json:"title"`
	Impact IncidentImpact `json:"impact"`
	Phase  IncidentPhase  `json:"phase"`

	Components []string `json:"components,omitempty"`

	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	Updates []PublicAnnouncementUpdate `json:"updates"`
}

// PublicAnnouncementUpdate é uma entrada pública da linha do tempo.
type PublicAnnouncementUpdate struct {
	Phase       IncidentPhase `json:"phase"`
	Body        string        `json:"body"`
	PublishedAt time.Time     `json:"published_at"`
}

// WorstOpenImpact devolve o maior impacto entre os relatos abertos.
//
// Só os abertos: um relato crítico resolvido na semana passada não pode
// manter o topo da página em vermelho. E é o maior, não o mais recente —
// um aviso menor publicado depois de uma queda crítica não acalma nada.
func WorstOpenImpact(announcements []PublicAnnouncement) IncidentImpact {
	pior := ImpactNone
	for _, a := range announcements {
		if a.Phase == PhaseResolved {
			continue
		}
		if a.Impact > pior {
			pior = a.Impact
		}
	}
	return pior
}
