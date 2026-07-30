// Package store define o contrato de persistência do UpWatch.
//
// A separação em dois contratos é deliberada. MetadataStore guarda
// definições — monitores, usuários, canais — e vive sempre num banco
// relacional. TimeseriesStore guarda o volume: batidas e rollups. Só o
// segundo precisa escalar, e é ele que poderá ser trocado por ClickHouse,
// InfluxDB ou remote-write sem tocar no resto do sistema.
//
// Qualquer implementação nova precisa passar integralmente na suíte
// storetest.RunConformance — é o que impede o "plugável" de virar fachada.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
)

// Limites de paginação. Listagem sem teto é o que degrada quando a tabela
// cresce, então toda consulta passa por Normalize antes de virar SQL.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

var (
	// ErrNotFound indica que o registro pedido não existe.
	ErrNotFound = errors.New("store: registro não encontrado")
	// ErrConflict indica violação de unicidade, como nome de monitor repetido.
	ErrConflict = errors.New("store: conflito de unicidade")
)

// PageFilter é a paginação por keyset. Keyset em vez de OFFSET porque
// páginas continuam estáveis quando linhas são inseridas ou removidas
// durante a navegação.
type PageFilter struct {
	// AfterID traz a página seguinte à do último ID visto. Zero começa do início.
	AfterID int64
	Limit   int
}

// Normalize aplica o limite padrão e trava o teto.
func (f PageFilter) Normalize() PageFilter {
	if f.Limit <= 0 {
		f.Limit = DefaultPageSize
	}
	if f.Limit > MaxPageSize {
		f.Limit = MaxPageSize
	}
	return f
}

// Page é uma fatia de resultados com indicação de continuação.
type Page[T any] struct {
	Items   []T
	HasMore bool
}

// TimeRange é uma janela semiaberta [From, To).
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Normalize converte a janela para UTC, que é como os timestamps são
// gravados; comparar contra horário local deslocaria o intervalo.
func (r TimeRange) Normalize() TimeRange {
	r.From = r.From.UTC()
	r.To = r.To.UTC()
	return r
}

// Valid informa se a janela tem início anterior ao fim.
func (r TimeRange) Valid() bool { return r.From.Before(r.To) }

// Contains testa pertinência com início inclusivo e fim exclusivo, de modo
// que buckets adjacentes não contem a mesma amostra duas vezes.
func (r TimeRange) Contains(ts time.Time) bool {
	return !ts.Before(r.From) && ts.Before(r.To)
}

// MonitorFilter seleciona monitores numa listagem.
type MonitorFilter struct {
	Page PageFilter
	// Enabled nulo traz habilitados e pausados.
	Enabled *bool
	Tag     string
}

// HeartbeatQuery seleciona batidas cruas.
type HeartbeatQuery struct {
	MonitorID int64
	// ProbeID vazio traz todas as origens.
	ProbeID string
	Range   TimeRange
	Limit   int
}

// Normalize aplica limites e normaliza a janela.
func (q HeartbeatQuery) Normalize() HeartbeatQuery {
	q.Range = q.Range.Normalize()
	q.Limit = PageFilter{Limit: q.Limit}.Normalize().Limit
	return q
}

// RollupQuery seleciona estatísticas agregadas.
type RollupQuery struct {
	MonitorID  int64
	ProbeID    string
	Resolution domain.Resolution
	Range      TimeRange
}

// Normalize preenche a resolução padrão e normaliza a janela.
func (q RollupQuery) Normalize() RollupQuery {
	if q.Resolution == 0 {
		q.Resolution = domain.ResolutionHourly
	}
	q.Range = q.Range.Normalize()
	return q
}

// MonitorRepo é o CRUD de definições de monitor.
type MonitorRepo interface {
	// Create insere o monitor e preenche ID, CreatedAt e UpdatedAt.
	// Devolve ErrConflict se o nome já existir.
	Create(ctx context.Context, m *domain.Monitor) error
	Get(ctx context.Context, id int64) (domain.Monitor, error)
	Update(ctx context.Context, m domain.Monitor) error
	// Delete remove o monitor e, em cascata, seu histórico.
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, f MonitorFilter) (Page[domain.Monitor], error)
}

// UserRepo é o CRUD de contas de acesso.
type UserRepo interface {
	Create(ctx context.Context, u *domain.User) error
	Get(ctx context.Context, id int64) (domain.User, error)
	// GetByUsername é o caminho do login.
	GetByUsername(ctx context.Context, username string) (domain.User, error)
	Update(ctx context.Context, u domain.User) error
	// Count decide se o assistente de primeiro acesso deve aparecer.
	Count(ctx context.Context) (int, error)

	List(ctx context.Context) ([]domain.User, error)
	Delete(ctx context.Context, id int64) error

	// CountByRole é o que impede a instalação de se trancar para fora:
	// remover ou rebaixar o último administrador deixaria a interface
	// sem ninguém capaz de criar outro.
	CountByRole(ctx context.Context, role domain.Role) (int, error)
}

// SessionRepo guarda os logins ativos da interface.
type SessionRepo interface {
	Create(ctx context.Context, s domain.Session) error
	// Get busca pelo hash; o cookie carrega o segredo cru.
	Get(ctx context.Context, hash string) (domain.Session, error)
	Delete(ctx context.Context, hash string) error
	// DeleteExpired é a limpeza periódica.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
	// DeleteByUser encerra todas as sessões de uma conta, usado quando a
	// senha muda: uma sessão anterior sobrevivendo à troca de senha
	// anularia o motivo de trocá-la.
	DeleteByUser(ctx context.Context, userID int64) error
}

// TokenRepo guarda as credenciais de acesso programático.
type TokenRepo interface {
	Create(ctx context.Context, t *domain.APIToken) error
	GetByHash(ctx context.Context, hash string) (domain.APIToken, error)
	List(ctx context.Context, userID int64) ([]domain.APIToken, error)
	Delete(ctx context.Context, id int64) error
	// TouchLastUsed registra o uso, para o operador identificar tokens
	// esquecidos antes de revogá-los.
	TouchLastUsed(ctx context.Context, id int64, at time.Time) error
}

// StateRepo guarda o estado confirmado de cada monitor.
//
// Persistir importa: sem isso um reinício zeraria a contagem de
// confirmação, e um alvo prestes a ser declarado fora do ar voltaria à
// estaca zero, atrasando a detecção em várias janelas.
type StateRepo interface {
	// Get devolve o estado, ou o zero quando o monitor ainda não foi
	// verificado.
	Get(ctx context.Context, monitorID int64) (domain.MonitorState, error)
	Save(ctx context.Context, monitorID int64, s domain.MonitorState) error
	// All carrega tudo de uma vez, para o motor não fazer uma consulta por
	// monitor no arranque.
	All(ctx context.Context) (map[int64]domain.MonitorState, error)
}

// IncidentFilter seleciona incidentes numa listagem.
type IncidentFilter struct {
	Page PageFilter
	// MonitorID zero traz de todos os monitores.
	MonitorID int64
	// OnlyOpen restringe aos que ainda não terminaram.
	OnlyOpen bool
}

// IncidentRepo guarda as janelas de indisponibilidade.
type IncidentRepo interface {
	// Open registra o começo de uma queda. Devolve ErrConflict se já
	// houver uma aberta para o monitor.
	Open(ctx context.Context, i *domain.Incident) error
	// Resolve encerra a queda aberta do monitor. Silencioso quando não há
	// nenhuma: encerrar o que já acabou não é erro.
	Resolve(ctx context.Context, monitorID int64, at time.Time) error
	// Current devolve a queda em curso, ou ErrNotFound.
	Current(ctx context.Context, monitorID int64) (domain.Incident, error)
	List(ctx context.Context, f IncidentFilter) (Page[domain.Incident], error)
}

// ChannelRepo guarda os destinos de aviso e seus vínculos.
type ChannelRepo interface {
	Create(ctx context.Context, c *domain.Channel) error
	Get(ctx context.Context, id int64) (domain.Channel, error)
	Update(ctx context.Context, c domain.Channel) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]domain.Channel, error)

	// Link e Unlink definem quais canais avisam sobre qual monitor.
	Link(ctx context.Context, monitorID, channelID int64) error
	Unlink(ctx context.Context, monitorID, channelID int64) error
	// ForMonitor devolve apenas os canais habilitados do monitor: um canal
	// desligado não deve receber nada, e filtrar aqui evita que cada
	// chamador precise lembrar disso.
	ForMonitor(ctx context.Context, monitorID int64) ([]domain.Channel, error)
}

// StatusPageRepo guarda as páginas públicas, seus grupos e componentes.
//
// Um contrato só, e não três, porque grupo e componente não existem fora
// de uma página: separá-los criaria três repositórios que só se usam
// juntos e nunca sozinhos.
type StatusPageRepo interface {
	// Create insere a página. Devolve ErrConflict se o slug já existir —
	// dois slugs iguais fariam duas páginas responderem no mesmo
	// endereço, e qual delas responde dependeria da ordem da varredura.
	Create(ctx context.Context, p *domain.StatusPage) error
	Get(ctx context.Context, id int64) (domain.StatusPage, error)
	// GetBySlug é o caminho da requisição anônima.
	GetBySlug(ctx context.Context, slug string) (domain.StatusPage, error)
	// GetDefault devolve a página que responde em "/status", ou
	// ErrNotFound quando nenhuma foi marcada.
	GetDefault(ctx context.Context) (domain.StatusPage, error)
	// SetDefault promove uma página a padrão e rebaixa a anterior, numa
	// transação. Trocar por erro de unicidade obrigaria quem opera a
	// desmarcar antes de marcar, o que é uma janela em que "/status" não
	// responde.
	SetDefault(ctx context.Context, id int64) error
	Update(ctx context.Context, p domain.StatusPage) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]domain.StatusPage, error)

	CreateGroup(ctx context.Context, g *domain.StatusPageGroup) error
	UpdateGroup(ctx context.Context, g domain.StatusPageGroup) error
	// DeleteGroup desfaz o agrupamento sem despublicar os componentes:
	// quem tira um grupo espera reorganizar, não remover da página.
	DeleteGroup(ctx context.Context, id int64) error
	Groups(ctx context.Context, pageID int64) ([]domain.StatusPageGroup, error)

	// SetComponent publica um monitor na página, ou atualiza o vínculo
	// existente. Idempotente: a interface reenvia o conjunto inteiro em
	// vez de calcular a diferença.
	SetComponent(ctx context.Context, c domain.StatusPageComponent) error
	RemoveComponent(ctx context.Context, pageID, monitorID int64) error
	Components(ctx context.Context, pageID int64) ([]domain.StatusPageComponent, error)
}

// AnnouncementFilter seleciona relatos numa listagem.
type AnnouncementFilter struct {
	Page PageFilter
	// Since corta a janela. Sem ele, uma instalação de dois anos
	// devolveria o histórico inteiro a cada visita anônima.
	Since time.Time
	// OnlyOpen restringe aos que ainda não foram resolvidos.
	OnlyOpen bool
}

// AnnouncementRepo guarda os relatos públicos e sua linha do tempo.
type AnnouncementRepo interface {
	Create(ctx context.Context, a *domain.Announcement) error
	Get(ctx context.Context, id int64) (domain.Announcement, error)
	// Update substitui os componentes em vez de acumulá-los: um relato
	// que agora só afeta o console não pode continuar aparecendo na
	// página que publica a API.
	Update(ctx context.Context, a domain.Announcement) error
	Delete(ctx context.Context, id int64) error
	// List devolve do mais recente para o mais antigo, que é como
	// "incidentes anteriores" se lê.
	List(ctx context.Context, f AnnouncementFilter) (Page[domain.Announcement], error)

	AddUpdate(ctx context.Context, u *domain.AnnouncementUpdate) error
	// Updates devolve em ordem cronológica: a linha do tempo de um
	// incidente se lê do começo para o fim.
	Updates(ctx context.Context, announcementID int64) ([]domain.AnnouncementUpdate, error)
}

// MetadataStore guarda as definições do sistema.
type MetadataStore interface {
	Monitors() MonitorRepo
	Users() UserRepo
	Sessions() SessionRepo
	Tokens() TokenRepo
	States() StateRepo
	Incidents() IncidentRepo
	Channels() ChannelRepo
	StatusPages() StatusPageRepo
	Announcements() AnnouncementRepo
	Close() error
}

// TimeseriesStore guarda o histórico: batidas cruas e agregados.
//
// É o contrato que backends alternativos precisam implementar.
type TimeseriesStore interface {
	// WriteHeartbeats grava um lote numa única transação. O batch writer
	// agrupa resultados justamente para não pagar um fsync por batida.
	WriteHeartbeats(ctx context.Context, hbs []domain.Heartbeat) error
	QueryHeartbeats(ctx context.Context, q HeartbeatQuery) ([]domain.Heartbeat, error)

	// StreamHeartbeats entrega todas as batidas da janela, em ordem
	// cronológica e sem teto de paginação.
	//
	// É o caminho da agregação, deliberadamente separado do da API. Um
	// bucket diário com check de um segundo tem 86.400 batidas; passar
	// pelo limite de paginação truncaria a amostra em silêncio e
	// produziria percentis que não descrevem período nenhum.
	//
	// Interromper devolvendo erro em fn aborta a varredura com esse erro.
	StreamHeartbeats(ctx context.Context, monitorID int64, r TimeRange, fn func(domain.Heartbeat) error) error

	// WriteRollups é idempotente: reprocessar um bucket sobrescreve em vez
	// de duplicar, para que uma reexecução após falha seja segura.
	WriteRollups(ctx context.Context, rs []domain.Rollup) error
	QueryRollups(ctx context.Context, q RollupQuery) ([]domain.Rollup, error)

	// PruneHeartbeats apaga batidas anteriores a before e devolve quantas
	// saíram. Só pode ser chamado depois da agregação do período — a ordem
	// inversa perderia o dado para sempre.
	PruneHeartbeats(ctx context.Context, before time.Time) (int64, error)
	PruneRollups(ctx context.Context, res domain.Resolution, before time.Time) (int64, error)

	// RecordPush registra o sinal recebido de um monitor push.
	//
	// Vive fora das batidas de propósito: o checker de push grava uma
	// batida a cada verificação, então ler a última batida seria ler a
	// que ele próprio acabou de escrever, e o monitor pareceria
	// eternamente saudável.
	RecordPush(ctx context.Context, monitorID int64, at time.Time) error

	// LastPush devolve o instante do último sinal, e se houve algum.
	LastPush(ctx context.Context, monitorID int64) (time.Time, bool, error)

	// LatestHeartbeats devolve a batida mais recente de cada monitor.
	//
	// Numa consulta só, e não uma por monitor: é o que a exposição de
	// métricas lê a cada raspagem, e uma consulta por alvo faria a
	// métrica virar a maior fonte de carga do banco que ela observa.
	LatestHeartbeats(ctx context.Context) (map[int64]domain.Heartbeat, error)

	// Compact devolve ao sistema o espaço liberado pela poda.
	//
	// Apagar linhas não encolhe o arquivo no SQLite: as páginas ficam
	// livres para reuso, mas o arquivo mantém o pico histórico. Sem esta
	// etapa o banco pararia de crescer sem nunca devolver espaço — é o
	// motivo de o Uptime Kuma expor um botão manual de VACUUM.
	//
	// Backends com manutenção automática, como o PostgreSQL, podem não
	// fazer nada aqui.
	Compact(ctx context.Context) error

	// OldestHeartbeat é o instante da batida mais antiga preservada, e se
	// existe alguma.
	//
	// É por onde a agregação começa quando não há marca d'água. Assumir
	// que nada mais velho que a janela de retenção sobreviveu seria errado
	// justamente no primeiro ciclo: a poda só roda depois da agregação,
	// então o dado antigo ainda está lá e precisa virar estatística antes
	// de sumir.
	OldestHeartbeat(ctx context.Context) (time.Time, bool, error)

	// RollupWatermark é o último bucket já agregado numa resolução.
	// Zero quando nada foi processado ainda.
	RollupWatermark(ctx context.Context, res domain.Resolution) (time.Time, error)
	SetRollupWatermark(ctx context.Context, res domain.Resolution, bucket time.Time) error

	Close() error
}

// Store reúne os dois contratos. Implementações SQL atendem aos dois com a
// mesma conexão; backends especializados podem atender só ao segundo.
type Store interface {
	MetadataStore
	TimeseriesStore
}
