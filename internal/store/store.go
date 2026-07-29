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

	"github.com/bernardojoao/upwatch/internal/domain"
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

// MetadataStore guarda as definições do sistema.
type MetadataStore interface {
	Monitors() MonitorRepo
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

	// WriteRollups é idempotente: reprocessar um bucket sobrescreve em vez
	// de duplicar, para que uma reexecução após falha seja segura.
	WriteRollups(ctx context.Context, rs []domain.Rollup) error
	QueryRollups(ctx context.Context, q RollupQuery) ([]domain.Rollup, error)

	// PruneHeartbeats apaga batidas anteriores a before e devolve quantas
	// saíram. Só pode ser chamado depois da agregação do período — a ordem
	// inversa perderia o dado para sempre.
	PruneHeartbeats(ctx context.Context, before time.Time) (int64, error)
	PruneRollups(ctx context.Context, res domain.Resolution, before time.Time) (int64, error)

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
