// Package sqlstore implementa store.Store sobre um banco relacional.
//
// O schema foi desenhado para que o mesmo SQL sirva a SQLite e PostgreSQL:
// tempo em milissegundos inteiros, enums em texto, agregação feita em Go.
// Sem funções de data específicas de dialeto, não há dois caminhos de
// código para divergirem.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/Jbnado/upwatch/internal/secretbox"
	"github.com/Jbnado/upwatch/internal/store"
	"github.com/Jbnado/upwatch/internal/store/migrations"

	_ "github.com/jackc/pgx/v5/stdlib" // driver PostgreSQL pure Go
	_ "modernc.org/sqlite"             // driver SQLite pure Go: sem CGO, binário estático
)

// Store é a implementação relacional de store.Store.
type Store struct {
	db      *db
	dialect migrations.Dialect
}

// OpenSQLite abre (ou cria) o banco SQLite em path e aplica as migrations.
//
// path pode ser um arquivo ou ":memory:". keys entrega a chave das
// credenciais depois de o store olhar o banco (ver secretbox.Source).
//
// Pragmas são passados na DSN
// porque valem por conexão — configurá-los uma vez só depois de abrir
// deixaria as demais conexões do pool sem eles, e a cascata de chaves
// estrangeiras silenciosamente desligada.
func OpenSQLite(path string, keys secretbox.Source) (*Store, error) {
	s, err := connectSQLite(path)
	if err != nil {
		return nil, err
	}
	if err := s.start(keys); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// connectSQLite abre a conexão, sem migrar nem destravar as credenciais.
func connectSQLite(path string) (*Store, error) {
	params := url.Values{}
	// Sem isto ON DELETE CASCADE não age e o histórico de um monitor
	// apagado vira lixo órfão. SQLite chega com a checagem desligada.
	params.Add("_pragma", "foreign_keys(1)")
	// WAL permite leitura concorrente com escrita — a API consulta
	// enquanto o batch writer grava.
	params.Add("_pragma", "journal_mode(WAL)")
	// Espera em vez de devolver SQLITE_BUSY na hora.
	params.Add("_pragma", "busy_timeout(10000)")
	// Com WAL, NORMAL é durável contra queda de processo e evita um fsync
	// por transação.
	params.Add("_pragma", "synchronous(NORMAL)")
	// Permite devolver espaço em lotes depois da poda, via Compact. Só
	// tem efeito se definido antes de a primeira tabela existir — trocar
	// depois exigiria um VACUUM completo, que reescreve o banco inteiro e
	// o trava enquanto isso.
	params.Add("_pragma", "auto_vacuum(incremental)")

	conn, err := sql.Open("sqlite", "file:"+path+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("sqlstore: abrindo %q: %w", path, err)
	}

	return &Store{db: &db{sql: conn, dialect: migrations.SQLite}, dialect: migrations.SQLite}, nil
}

// OpenPostgres abre a conexão com PostgreSQL e aplica as migrations.
//
// É o caminho de produção: SQLite dá conta de uma instalação inteira num
// arquivo, mas não de duas instâncias do UpWatch escrevendo ao mesmo
// tempo, que é o que se quer quando a disponibilidade do monitorador
// também importa.
//
// O mesmo SQL serve aos dois bancos. As duas diferenças que o schema não
// resolve — marcador de parâmetro e descoberta do id gerado — vivem em
// db.go, na borda, e não espalhadas pelos repositórios.
func OpenPostgres(dsn string, keys secretbox.Source) (*Store, error) {
	s, err := connectPostgres(dsn)
	if err != nil {
		return nil, err
	}
	if err := s.start(keys); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// connectPostgres abre a conexão, sem migrar nem destravar as
// credenciais.
func connectPostgres(dsn string) (*Store, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: abrindo postgres: %w", err)
	}

	// Teto de conexões: o padrão do database/sql é ilimitado, e uma
	// instalação com muitos monitores abriria conexões até o servidor
	// recusar — falha que aparece como erro de banco no meio de um
	// incidente, quando a carga é maior.
	conn.SetMaxOpenConns(maxPostgresConns)
	conn.SetMaxIdleConns(maxPostgresConns)
	conn.SetConnMaxLifetime(time.Hour)

	// Verifica antes de migrar: um erro de DSN precisa aparecer como
	// "não consegui conectar", e não como uma migration que falhou pela
	// metade.
	ctx, cancel := context.WithTimeout(context.Background(), postgresPingTimeout)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sqlstore: conectando ao postgres: %w", err)
	}

	return &Store{db: &db{sql: conn, dialect: migrations.Postgres}, dialect: migrations.Postgres}, nil
}

const (
	// maxPostgresConns limita o pool.
	maxPostgresConns = 16

	// postgresPingTimeout é quanto se espera pela primeira resposta.
	postgresPingTimeout = 10 * time.Second
)

// Open escolhe a implementação pelo driver configurado.
//
// Existe para o binário não precisar de um switch na inicialização, e
// para "banco plugável" ter um ponto de entrada só.
func Open(driver, dsn string, keys secretbox.Source) (*Store, error) {
	switch driver {
	case string(migrations.SQLite):
		return OpenSQLite(dsn, keys)
	case string(migrations.Postgres):
		return OpenPostgres(dsn, keys)
	default:
		return nil, unknownDriver(driver)
	}
}

// connect abre a conexão do driver configurado, sem migrar nem destravar
// as credenciais. Serve a quem precisa mexer no banco sem a chave.
func connect(driver, dsn string) (*Store, error) {
	switch driver {
	case string(migrations.SQLite):
		return connectSQLite(dsn)
	case string(migrations.Postgres):
		return connectPostgres(dsn)
	default:
		return nil, unknownDriver(driver)
	}
}

func unknownDriver(driver string) error {
	return fmt.Errorf("sqlstore: driver desconhecido %q; use sqlite ou postgres", driver)
}

// start migra o schema e destrava as credenciais. Nenhuma das duas
// etapas é opcional: um store que abrisse sem chave gravaria a primeira
// senha em texto puro.
func (s *Store) start(keys secretbox.Source) error {
	if err := s.migrate(); err != nil {
		return err
	}
	return s.unlockSecrets(keys)
}

// migrate aplica todas as migrations pendentes do dialeto.
func (s *Store) migrate() error {
	fsys, err := migrations.FS(s.dialect)
	if err != nil {
		return err
	}

	goose.SetBaseFS(fsys)
	goose.SetLogger(goose.NopLogger())
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect(gooseDialect(s.dialect)); err != nil {
		return fmt.Errorf("sqlstore: dialeto goose: %w", err)
	}
	if err := goose.Up(s.db.sql, "."); err != nil {
		return fmt.Errorf("sqlstore: aplicando migrations: %w", err)
	}
	return nil
}

// Rollback desfaz a última migration. Existe para que o teste consiga
// verificar que o schema desce, não só sobe.
func (s *Store) Rollback() error {
	return s.down(func() error { return goose.Down(s.db.sql, ".") })
}

// RollbackAll desfaz todas as migrations, na ordem inversa.
//
// É o que exercita o Down de cada uma. Verificar só a última deixaria
// passar uma migration antiga sem reversão, e o defeito só apareceria no
// dia em que alguém precisasse voltar uma versão — que é justamente o
// pior dia para descobrir.
func (s *Store) RollbackAll() error {
	return s.down(func() error { return goose.DownTo(s.db.sql, ".", 0) })
}

// RollbackTo desce o schema até a versão indicada.
//
// Existe para o teste de migração montar um banco na forma antiga e
// conferir que a subida preserva o que havia nele.
func (s *Store) RollbackTo(version int64) error {
	return s.down(func() error { return goose.DownTo(s.db.sql, ".", version) })
}

func (s *Store) down(run func() error) error {
	fsys, err := migrations.FS(s.dialect)
	if err != nil {
		return err
	}

	goose.SetBaseFS(fsys)
	goose.SetLogger(goose.NopLogger())
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect(gooseDialect(s.dialect)); err != nil {
		return fmt.Errorf("sqlstore: dialeto goose: %w", err)
	}
	if err := run(); err != nil {
		return fmt.Errorf("sqlstore: revertendo migration: %w", err)
	}
	return nil
}

func gooseDialect(d migrations.Dialect) string {
	if d == migrations.Postgres {
		return "postgres"
	}
	return "sqlite3"
}

// DB expõe a conexão para inspeção em teste (planos de consulta, contagens).
func (s *Store) DB() *sql.DB { return s.db.sql }

// Monitors devolve o repositório de definições de monitor.
func (s *Store) Monitors() store.MonitorRepo { return &monitorRepo{db: s.db} }

// Users devolve o repositório de contas de acesso.
func (s *Store) Users() store.UserRepo { return &userRepo{db: s.db} }

// Sessions devolve o repositório de logins ativos.
func (s *Store) Sessions() store.SessionRepo { return &sessionRepo{db: s.db} }

// Tokens devolve o repositório de credenciais programáticas.
func (s *Store) Tokens() store.TokenRepo { return &tokenRepo{db: s.db} }

// TwoFactor devolve o repositório do segundo fator.
func (s *Store) TwoFactor() store.TwoFactorRepo { return &twoFactorRepo{db: s.db} }

// States devolve o repositório do estado confirmado dos monitores.
func (s *Store) States() store.StateRepo { return &stateRepo{db: s.db} }

// Incidents devolve o repositório de janelas de indisponibilidade.
func (s *Store) Incidents() store.IncidentRepo { return &incidentRepo{db: s.db} }

// Channels devolve o repositório de destinos de aviso.
func (s *Store) Channels() store.ChannelRepo { return &channelRepo{db: s.db} }

// StatusPages devolve o repositório das páginas públicas.
func (s *Store) StatusPages() store.StatusPageRepo { return &statusPageRepo{db: s.db} }

// Announcements devolve o repositório dos relatos públicos.
func (s *Store) Announcements() store.AnnouncementRepo { return &announcementRepo{db: s.db} }

// Close encerra a conexão.
func (s *Store) Close() error { return s.db.sql.Close() }

// Groups devolve o repositório de grupos de monitores.
func (s *Store) Groups() store.GroupRepo { return &groupRepo{db: s.db} }

// Transitions devolve o histórico de mudanças de estado.
func (s *Store) Transitions() store.TransitionRepo { return &transitionRepo{db: s.db} }

// ---------- conversões compartilhadas ----------

// toMillis grava tempo como inteiro de milissegundos UTC. Escolha
// deliberada: mantém o SQL idêntico entre dialetos, já que nenhum tipo de
// data precisa ser negociado.
func toMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// boolToInt existe porque SQLite não tem tipo booleano; o schema Postgres
// usa INTEGER pelo mesmo motivo, para não haver dois caminhos de código.
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// inTx roda fn numa transação, revertendo em caso de erro ou panic.
func (s *Store) inTx(ctx context.Context, fn func(*tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: iniciando transação: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, rbErr)
		}
		return err
	}
	return tx.Commit()
}
