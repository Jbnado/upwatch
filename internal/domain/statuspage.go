package domain

import (
	"fmt"
	"strings"
	"time"
)

// A página pública de estado.
//
// É a única superfície do UpWatch que qualquer pessoa alcança sem
// credencial, e por isso o desenho aqui é subtrativo: em vez de reusar
// Monitor e lembrar de remover campos, existem tipos próprios que não
// têm onde guardar o que não deve sair. Endereço do alvo e causa da
// falha descrevem a topologia interna — quem abriu o link quer saber se
// o serviço está no ar, não em qual porta ele escuta.

const (
	// maxSlugLength limita o identificador que vai na URL.
	maxSlugLength = 64

	// maxStatusPageTitleLength limita o título exibido.
	maxStatusPageTitleLength = 120
)

// StatusPage é uma página pública de estado.
//
// Várias, e não uma global: quem opera para clientes diferentes precisa
// mostrar recortes diferentes da mesma infraestrutura, e essa é a
// primeira coisa que se pede a uma ferramenta que só tem uma.
type StatusPage struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`

	Title       string `json:"title"`
	Description string `json:"description,omitempty"`

	// ShowLatency libera os números de latência na página.
	//
	// Desligado por padrão: latência de resposta é informação competitiva
	// para parte de quem publica, e a pergunta "está no ar?" não depende
	// dela.
	ShowLatency bool `json:"show_latency"`

	// TimeZone é o fuso em que a página exibe horários.
	//
	// Vale para o carimbo dos relatos e da última atualização. As barras
	// de histórico continuam recortadas por dia UTC, que é como os
	// agregados diários são gravados: recortá-las por fuso exigiria ler
	// os agregados horários dos noventa dias, e a retenção horária padrão
	// é justamente noventa dias — as barras mais antigas ficariam vazias
	// numa instalação antiga, que é pior do que uma fronteira de dia
	// deslocada em algumas horas.
	TimeZone string `json:"time_zone,omitempty"`

	// Enabled desligada devolve 404, não 403: negar com "existe, mas não
	// para você" confirmaria a existência da página a quem só chutou o
	// endereço.
	Enabled bool `json:"enabled"`

	// Default responde em "/status", sem slug.
	//
	// Uma instalação com uma página só não deveria obrigar ninguém a
	// digitar "/status/estado" — o slug repete o que o caminho já diz. No
	// máximo uma página é padrão, e o banco recusa a segunda.
	Default bool `json:"default"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate confere as invariantes da página.
func (p StatusPage) Validate() error {
	if err := ValidateSlug(p.Slug); err != nil {
		return err
	}

	title := strings.TrimSpace(p.Title)
	if title == "" {
		return invalid("title", "não pode ser vazio")
	}
	if len(title) > maxStatusPageTitleLength {
		return invalid("title", fmt.Sprintf("não pode passar de %d caracteres", maxStatusPageTitleLength))
	}
	if p.TimeZone != "" {
		if _, err := time.LoadLocation(p.TimeZone); err != nil {
			return invalid("time_zone", "fuso horário desconhecido")
		}
	}
	return nil
}

// Location devolve o fuso da página, ou UTC quando não configurado.
func (p StatusPage) Location() *time.Location {
	if p.TimeZone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		// Validate já recusou o fuso desconhecido na entrada; se um valor
		// inválido chegou aqui, exibir em UTC é melhor que entrar em
		// pânico numa página pública.
		return time.UTC
	}
	return loc
}

// ValidateSlug confere o identificador que vai na URL.
//
// Estrito de propósito. O slug é colado em chat, digitado de memória e
// concatenado em caminho: barra e ponto-ponto viram travessia, acento e
// espaço viram sequência de escape que ninguém reconhece, e maiúscula
// cria dois endereços para a mesma página em sistemas que diferenciam
// caixa.
func ValidateSlug(slug string) error {
	if strings.TrimSpace(slug) == "" {
		return invalid("slug", "não pode ser vazio")
	}
	if slug != strings.TrimSpace(slug) {
		return invalid("slug", "não pode começar nem terminar com espaço")
	}
	if len(slug) > maxSlugLength {
		return invalid("slug", fmt.Sprintf("não pode passar de %d caracteres", maxSlugLength))
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return invalid("slug", "não pode começar nem terminar com hífen")
	}
	// Hífen duplo é a assinatura de um slug gerado a partir de um título
	// com pontuação. Recusar aqui evita "estado--da--plataforma" nascer de
	// um descuido e virar endereço permanente.
	if strings.Contains(slug, "--") {
		return invalid("slug", "não pode ter dois hífens seguidos")
	}

	for _, r := range slug {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return invalid("slug", "use apenas letras minúsculas sem acento, números e hífen")
		}
	}
	return nil
}

// StatusPageGroup agrupa componentes na página.
//
// As páginas que servem de referência — Anthropic, Cloudflare, Google —
// todas agrupam: "API", "Console", "Webhooks". Sem grupo, uma
// instalação com quarenta alvos publica uma lista de quarenta linhas
// onde ninguém acha o que veio procurar.
type StatusPageGroup struct {
	ID     int64  `json:"id"`
	PageID int64  `json:"page_id"`
	Name   string `json:"name"`

	// Position ordena os grupos. A ordem é editorial: o que o cliente
	// mais usa vem primeiro, e isso não se deduz de dado nenhum.
	Position int `json:"position"`
}

// Validate confere as invariantes do grupo.
func (g StatusPageGroup) Validate() error {
	if strings.TrimSpace(g.Name) == "" {
		return invalid("name", "não pode ser vazio")
	}
	if len(g.Name) > maxStatusPageTitleLength {
		return invalid("name", fmt.Sprintf("não pode passar de %d caracteres", maxStatusPageTitleLength))
	}
	return nil
}

// StatusPageComponent liga um monitor a uma página.
//
// Label existe para o nome público divergir do interno: o monitor pode
// se chamar "api-prod-us-east-1" na operação e aparecer como "API" para
// quem lê. Sem isso, publicar a página obrigaria a renomear o monitor —
// ou a entregar a convenção de nomes da infraestrutura.
type StatusPageComponent struct {
	PageID    int64 `json:"page_id"`
	MonitorID int64 `json:"monitor_id"`

	// GroupID nulo põe o componente fora de qualquer grupo, listado
	// antes dos grupos.
	GroupID *int64 `json:"group_id,omitempty"`

	Label    string `json:"label,omitempty"`
	Position int    `json:"position"`
}

// Validate confere as invariantes do componente.
func (c StatusPageComponent) Validate() error {
	if c.MonitorID == 0 {
		return invalid("monitor_id", "não pode ser vazio")
	}
	if len(c.Label) > maxStatusPageTitleLength {
		return invalid("label", fmt.Sprintf("não pode passar de %d caracteres", maxStatusPageTitleLength))
	}
	return nil
}

// PublicMonitor é o que a página revela sobre um alvo.
//
// Note o que não existe aqui: Target, Config, ParentID, e a mensagem de
// erro do check. É um tipo próprio justamente para que o vazamento exija
// acrescentar um campo, e não apenas esquecer de removê-lo.
type PublicMonitor struct {
	Name   string `json:"name"`
	Status Status `json:"status"`

	// UptimePercent ausente quando nada foi observado na janela. Zero
	// afirmaria queda total, que é a leitura mais alarmante possível.
	UptimePercent *float64 `json:"uptime_percent,omitempty"`

	// LatencyP95Ms só aparece se a página liberar latência.
	LatencyP95Ms *int64 `json:"latency_p95_ms,omitempty"`

	// History é um dia por elemento, do mais antigo ao mais recente.
	History []PublicDay `json:"history"`
}

// PublicDay é um dia da barra de histórico.
type PublicDay struct {
	// Date é o dia em AAAA-MM-DD. String e não time.Time porque é um dia
	// de calendário, não um instante: enviar um carimbo faria cada
	// navegador reinterpretá-lo no seu próprio fuso e a mesma barra
	// mudaria de lugar dependendo de quem olha.
	Date   string `json:"date"`
	Status Status `json:"status"`

	UptimePercent *float64 `json:"uptime_percent,omitempty"`
}

// PublicGroup é uma seção da página.
type PublicGroup struct {
	// Name vazio é o grupo implícito dos componentes sem agrupamento,
	// sempre o primeiro. Vem como grupo, e não como lista à parte, para o
	// cliente ter um laço só.
	Name     string          `json:"name"`
	Monitors []PublicMonitor `json:"monitors"`
}

// PublicView é a página inteira como sai para quem não tem credencial.
//
// Envelope próprio, como os demais tipos públicos: nada aqui carrega
// identificador interno, endereço de alvo ou contagem de monitores da
// instalação.
type PublicView struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	TimeZone    string `json:"time_zone,omitempty"`

	// Status resume os componentes; Impact resume os relatos abertos. São
	// duas perguntas diferentes: o primeiro é o que as sondas medem, o
	// segundo é o que uma pessoa declarou.
	Status Status         `json:"status"`
	Impact IncidentImpact `json:"impact"`

	Groups        []PublicGroup        `json:"groups"`
	Announcements []PublicAnnouncement `json:"announcements"`

	// WindowDays é quantos dias as barras cobrem, para a legenda não
	// precisar adivinhar.
	WindowDays  int       `json:"window_days"`
	GeneratedAt time.Time `json:"generated_at"`
}

// OverallStatus resume a página numa palavra.
//
// Ordena por gravidade e toma a pior: queda domina, lentidão vem em
// seguida, e "sem medição" fica abaixo de "no ar" de propósito — um
// monitor push recém-criado não pode fazer a página inteira dizer que
// não sabe nada enquanto o resto responde. Ele aparece como sem medição
// na própria linha, e o topo continua contando a verdade.
func OverallStatus(statuses []Status) Status {
	gravidade := map[Status]int{
		StatusUnknown:  0,
		StatusUp:       1,
		StatusDegraded: 2,
		StatusDown:     3,
	}

	pior := StatusUnknown
	for _, s := range statuses {
		if gravidade[s] > gravidade[pior] {
			pior = s
		}
	}
	return pior
}
