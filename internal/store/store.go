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
	// ErrLastFullAdmin recusa a escrita que deixaria a instalação sem
	// nenhum administrador de escopo total.
	ErrLastFullAdmin = errors.New("store: a instalação ficaria sem administrador de escopo total")
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

// GroupSelection restringe uma listagem aos monitores de certos grupos.
//
// Ponteiro nos filtros: nulo não restringe, e uma seleção vazia não traz
// nada. Confundir os dois faria uma conta com escopo esvaziado enxergar a
// instalação inteira.
type GroupSelection struct {
	// IDs são os grupos aceitos. O store não expande sistema em
	// ambientes: quem monta a seleção já entrega a lista resolvida.
	IDs []int64
	// Ungrouped inclui os monitores sem grupo.
	Ungrouped bool
}

// MonitorFilter seleciona monitores numa listagem.
type MonitorFilter struct {
	Page PageFilter
	// Enabled nulo traz habilitados e pausados.
	Enabled *bool
	Tag     string
	// Groups nulo traz de todos os grupos.
	Groups *GroupSelection
}

// HeartbeatQuery seleciona batidas cruas.
type HeartbeatQuery struct {
	MonitorID int64
	// ProbeID vazio traz todas as origens.
	ProbeID string
	Range   TimeRange
	Limit   int
	// OnlyFailures restringe às batidas fora do ar e degradadas. O filtro
	// acontece antes do limite, senão uma janela de batidas boas
	// esconderia a falha que veio antes delas.
	OnlyFailures bool
	// Before, quando preenchido, traz só as batidas anteriores ao cursor
	// na ordem (instante, origem) decrescente. É como a linha do tempo
	// pagina para trás: cortar só pelo instante perdia as batidas de
	// outras origens que caíam no mesmo milissegundo da última da página.
	Before *HeartbeatCursor
}

// HeartbeatCursor é a posição de uma batida na ordem (instante, origem).
type HeartbeatCursor struct {
	At      time.Time
	ProbeID string
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
	// Devolve ErrConflict se o nome já existir no grupo, e ErrNotFound se
	// o grupo (ou o monitor pai) não existir — o que Update também faz.
	Create(ctx context.Context, m *domain.Monitor) error
	Get(ctx context.Context, id int64) (domain.Monitor, error)
	Update(ctx context.Context, m domain.Monitor) error
	// Delete remove o monitor e, em cascata, seu histórico.
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, f MonitorFilter) (Page[domain.Monitor], error)

	// SetParent põe os monitores ids sob o pai (nil desliga), tudo ou
	// nada: um id ou o pai inexistente devolvem ErrNotFound sem gravar
	// nenhum. Não mexe no resto da linha, e por isso não recomeça a
	// espera do push. Ciclos são decisão de quem chama (ParentCycle).
	SetParent(ctx context.Context, parentID *int64, ids []int64) error

	// FindByPushToken devolve o monitor dono do token de push, ou
	// ErrNotFound. Token vazio não acha ninguém.
	//
	// É o caminho do /push, que não tem login: a busca é indexada para
	// uma requisição qualquer não custar uma varredura da tabela. O token
	// é único entre os monitores — gravar um repetido devolve ErrConflict.
	FindByPushToken(ctx context.Context, token string) (domain.Monitor, error)
}

// UserRepo é o CRUD de contas de acesso.
type UserRepo interface {
	Create(ctx context.Context, u *domain.User) error
	Get(ctx context.Context, id int64) (domain.User, error)
	// GetByUsername é o caminho do login.
	GetByUsername(ctx context.Context, username string) (domain.User, error)

	// SetPasswordHash grava a senha e nada mais.
	//
	// Não existe "gravar a conta inteira": quem lê a conta, decide e
	// regrava tudo desfaz o que outro pedido gravou no meio — a troca de
	// senha devolvia o escopo que um administrador acabara de retirar, e
	// a troca de acesso devolvia a senha que a pessoa acabara de trocar.
	SetPasswordHash(ctx context.Context, id int64, hash string) error

	// SetAccess grava o papel e, quando não nulo, o escopo — nada mais.
	// Escopo nulo mantém o gravado.
	//
	// Devolve ErrLastFullAdmin se a escrita deixaria a instalação sem
	// administrador de escopo total, conferido na mesma transação: conferir
	// antes deixava dois rebaixamentos simultâneos verem "sobra o outro".
	SetAccess(ctx context.Context, id int64, role domain.Role, scope *domain.Scope) error
	// Count decide se o assistente de primeiro acesso deve aparecer.
	Count(ctx context.Context) (int, error)

	List(ctx context.Context) ([]domain.User, error)
	// Delete apaga a conta. Devolve ErrLastFullAdmin, como SetAccess, se
	// ela for o último administrador de escopo total.
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
	// Limit é o tamanho da página, normalizado como em PageFilter.
	Limit int
	// BeforeID pagina para trás: só incidentes com id menor. A lista sai
	// do mais recente para o mais antigo, e o cursor da página seguinte é
	// o id do último item desta. Zero começa do mais recente.
	BeforeID int64
	// MonitorID zero traz de todos os monitores.
	MonitorID int64
	// OnlyOpen restringe aos que ainda não terminaram.
	OnlyOpen bool
	// Groups nulo traz de todos os grupos.
	Groups *GroupSelection
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
	// Get devolve o incidente pelo id, ou ErrNotFound.
	Get(ctx context.Context, id int64) (domain.Incident, error)
	List(ctx context.Context, f IncidentFilter) (Page[domain.Incident], error)

	// Acknowledge registra o "estou vendo" e devolve o incidente como
	// ficou. ErrNotFound se ele não existir; ErrConflict se já estiver
	// encerrado ou reconhecido — o primeiro reconhecimento é o que conta,
	// e trocá-lo calado apagaria quem chegou antes.
	Acknowledge(ctx context.Context, id int64, ack domain.Acknowledgement) (domain.Incident, error)
	// MarkReminded registra o lembrete enviado em at, e só se o incidente
	// seguir aberto, sem reconhecimento e com o último lembrete ainda em
	// previous (nulo: nenhum). Devolve se registrou.
	//
	// A condição é conferida na própria escrita, e quem chama só envia o
	// lembrete se ela passou: um reconhecimento que chega entre a leitura
	// e o envio cala o lembrete em vez de perder para ele.
	MarkReminded(ctx context.Context, id int64, previous *time.Time, at time.Time) (bool, error)
}

// ChannelRepo guarda os destinos de aviso e seus vínculos.
type ChannelRepo interface {
	Create(ctx context.Context, c *domain.Channel) error
	Get(ctx context.Context, id int64) (domain.Channel, error)
	Update(ctx context.Context, c domain.Channel) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]domain.Channel, error)
	// ListFor devolve, em ordem de id, os canais que avisam sobre algo da
	// seleção: vinculados a um monitor dos grupos escolhidos ou ao próprio
	// grupo. Inclui os desligados, como List. É a listagem de quem tem
	// escopo: o canal que só avisa sobre os outros revela, pelo nome e pelo
	// destino, o que existe fora.
	ListFor(ctx context.Context, sel GroupSelection) ([]domain.Channel, error)

	// Link e Unlink definem quais canais avisam sobre qual monitor. Link
	// devolve ErrNotFound se o canal ou o monitor não existir.
	Link(ctx context.Context, monitorID, channelID int64) error
	Unlink(ctx context.Context, monitorID, channelID int64) error
	// ForMonitor devolve apenas os canais habilitados do monitor: um canal
	// desligado não deve receber nada, e filtrar aqui evita que cada
	// chamador precise lembrar disso.
	ForMonitor(ctx context.Context, monitorID int64) ([]domain.Channel, error)

	// LinkGroup e UnlinkGroup definem quais canais avisam sobre todos os
	// monitores de um grupo, inclusive os criados depois. LinkGroup
	// devolve ErrNotFound se o canal ou o grupo não existir.
	LinkGroup(ctx context.Context, groupID, channelID int64) error
	UnlinkGroup(ctx context.Context, groupID, channelID int64) error
	// ForGroup devolve os canais habilitados vinculados direto ao grupo.
	ForGroup(ctx context.Context, groupID int64) ([]domain.Channel, error)

	// Destinations é para onde vai o aviso de um monitor: os canais
	// habilitados vinculados a ele, ao grupo dele e ao sistema do grupo,
	// cada um uma vez só.
	//
	// Uma consulta, e não três somadas por quem chama: a regra de quem é
	// avisado precisa morar num lugar só, senão o motor e a tela de canais
	// passam a discordar sobre quem recebe.
	Destinations(ctx context.Context, monitorID int64) ([]domain.Channel, error)
}

// MaintenanceRepo guarda as janelas de manutenção.
//
// Os alvos (monitores e grupos) saem por cascata quando o monitor ou o
// grupo é apagado: a janela fica com menos alcance, nunca com outro.
type MaintenanceRepo interface {
	Create(ctx context.Context, w *domain.MaintenanceWindow) error
	Get(ctx context.Context, id int64) (domain.MaintenanceWindow, error)
	// Update regrava a definição e os alvos, sem tocar em EndedAt: o
	// encerramento tem caminho próprio (End), para uma edição feita com a
	// tela aberta desde antes não reabrir a janela que alguém encerrou.
	Update(ctx context.Context, w domain.MaintenanceWindow) error
	Delete(ctx context.Context, id int64) error
	// List traz todas, da que começa mais tarde para a mais antiga. São
	// poucas, e o motor precisa de todas para decidir.
	List(ctx context.Context) ([]domain.MaintenanceWindow, error)
	// End encerra a janela no instante, ou mantém o encerramento anterior
	// se ele for mais cedo.
	End(ctx context.Context, id int64, at time.Time) error
}

// GroupRepo guarda os grupos de monitores.
//
// A forma da árvore — dois níveis, só folha recebe monitor — é conferida
// no domínio (domain.GroupTree), que dá a mensagem para quem pediu. Aqui
// ficam as garantias que só o banco dá sem corrida: unicidade entre
// irmãos, a profundidade de dois níveis e a recusa de apagar o que está
// em uso.
type GroupRepo interface {
	// Create devolve ErrConflict se já houver irmão com o mesmo nome ou
	// slug, ou se o pai não for de topo; ErrNotFound se o pai não existir.
	Create(ctx context.Context, g *domain.Group) error
	Get(ctx context.Context, id int64) (domain.Group, error)
	// Update recusa como Create, e também quando o grupo que ganha pai tem
	// ambientes dentro dele.
	Update(ctx context.Context, g domain.Group) error
	// Delete devolve ErrConflict enquanto o grupo tiver monitores ou
	// ambientes: apagar perderia o vínculo deles em silêncio. Vínculos de
	// canal e concessões de escopo saem junto.
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]domain.Group, error)
}

// TransitionFilter seleciona mudanças de estado de um monitor.
type TransitionFilter struct {
	MonitorID int64
	// Before corta a página: só mudanças anteriores a este instante. Zero
	// começa do agora.
	Before time.Time
	// BeforeID desempata no instante Before: com ele, entram também as
	// mudanças desse instante com id menor. A lista sai em (instante, id)
	// decrescente, e sem o desempate a página seguinte pulava as mudanças
	// que dividiam o instante com a última da página anterior.
	BeforeID int64
	Limit    int
}

// TransitionRepo guarda o histórico de mudanças confirmadas de estado.
type TransitionRepo interface {
	Record(ctx context.Context, t *domain.Transition) error
	// List devolve da mais recente para a mais antiga, que é como uma
	// linha do tempo de falhas se lê.
	List(ctx context.Context, f TransitionFilter) ([]domain.Transition, error)
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
	Groups() GroupRepo
	Transitions() TransitionRepo
	Maintenance() MaintenanceRepo
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
	//
	// Guarda só o último sinal, inteiro: status, causa e latência
	// substituem os do anterior, para uma causa velha não grudar num sinal
	// saudável.
	RecordPush(ctx context.Context, monitorID int64, sig domain.PushSignal) error

	// LastPush devolve o último sinal, e se houve algum.
	LastPush(ctx context.Context, monitorID int64) (domain.PushSignal, bool, error)

	// MarkPushEvaluated registra que o sinal signalID já foi contado por
	// uma verificação.
	//
	// O sinal fica guardado até chegar o seguinte, e cada verificação o
	// relê. Sem a marca, o mesmo "down" contaria como falha nova a cada
	// releitura, e um sinal só abriria incidente sozinho. Marcar um sinal
	// que já foi substituído não tem efeito: a marca é do sinal, e o novo
	// continua inédito.
	MarkPushEvaluated(ctx context.Context, monitorID, signalID int64) error

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
