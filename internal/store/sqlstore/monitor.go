package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

type monitorRepo struct {
	db *db
}

const monitorColumns = `
	id, name, type, target, interval_ms, timeout_ms,
	confirmation_threshold, degraded_latency_ms, config, parent_id,
	enabled, tags, created_at, updated_at, rearmed_at, push_token, group_id`

// emptyConfig é o que grava quando o monitor não traz configuração. Nil ou
// string vazia quebrariam o Unmarshal que o checker faz na leitura.
const emptyConfig = "{}"

func encodeConfig(cfg json.RawMessage) string {
	if len(cfg) == 0 {
		return emptyConfig
	}
	return string(cfg)
}

// Create insere o monitor e preenche ID e timestamps.
func (r *monitorRepo) Create(ctx context.Context, m *domain.Monitor) error {
	tags, err := encodeTags(m.Tags)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	m.CreatedAt, m.UpdatedAt, m.RearmedAt = now, now, now

	// INSERT ... SELECT com a existência do grupo na própria instrução.
	// No SQLite group_id não tem chave estrangeira (ver a migração), e
	// conferir antes, numa consulta separada, deixava o grupo ser apagado
	// no meio: o monitor ficava órfão, invisível para todo escopo e fora
	// de "Sem grupo". O SQLite serializa as escritas, então a condição e a
	// gravação não se separam. No PostgreSQL a chave estrangeira segura a
	// corrida que o instantâneo da condição não vê.
	const q = `
		INSERT INTO monitor (
			name, type, target, interval_ms, timeout_ms,
			confirmation_threshold, degraded_latency_ms, config, parent_id,
			enabled, tags, created_at, updated_at, rearmed_at, push_token, group_id
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE ` + groupExists

	id, err := r.db.insertID(ctx, q,
		m.Name, m.Type.String(), m.Target,
		m.Interval.Milliseconds(), m.Timeout.Milliseconds(),
		m.ConfirmationThreshold, m.DegradedLatency.Milliseconds(),
		encodeConfig(m.Config), m.ParentID,
		boolToInt(m.Enabled), tags, toMillis(now), toMillis(now), toMillis(now),
		nullableToken(m.PushToken), m.GroupID,
		m.GroupID, m.GroupID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("grupo %d: %w", *m.GroupID, store.ErrNotFound)
	}
	if err != nil {
		return translateWriteErr(err)
	}
	m.ID = id
	return nil
}

// Get devolve um monitor pelo id.
func (r *monitorRepo) Get(ctx context.Context, id int64) (domain.Monitor, error) {
	row := r.db.QueryRowContext(ctx, `SELECT`+monitorColumns+` FROM monitor WHERE id = ?`, id)

	m, err := scanMonitor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Monitor{}, fmt.Errorf("monitor %d: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return domain.Monitor{}, err
	}
	return m, nil
}

// Update sobrescreve os campos editáveis do monitor.
//
// rearmed_at avança quando a configuração ou o intervalo mudam, ou quando
// o monitor é religado — o que recomeça a espera do push. A comparação
// acontece no próprio UPDATE, contra a linha como estava: as expressões do
// SET leem os valores antigos nos dois dialetos, e assim duas edições
// simultâneas não decidem com base numa leitura velha.
func (r *monitorRepo) Update(ctx context.Context, m domain.Monitor) error {
	tags, err := encodeTags(m.Tags)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	config := encodeConfig(m.Config)
	intervalMS := m.Interval.Milliseconds()
	enabled := boolToInt(m.Enabled)

	// A mesma condição de Create: mover para um grupo apagado no meio do
	// pedido não grava nada.
	const q = `
		UPDATE monitor SET
			rearmed_at = CASE
				WHEN config <> ? OR interval_ms <> ? OR (enabled = 0 AND ? = 1) THEN ?
				ELSE rearmed_at
			END,
			name = ?, type = ?, target = ?, interval_ms = ?, timeout_ms = ?,
			confirmation_threshold = ?, degraded_latency_ms = ?, config = ?, parent_id = ?,
			enabled = ?, tags = ?, updated_at = ?, push_token = ?, group_id = ?
		WHERE id = ? AND ` + groupExists

	res, err := r.db.ExecContext(ctx, q,
		config, intervalMS, enabled, toMillis(now),
		m.Name, m.Type.String(), m.Target,
		intervalMS, m.Timeout.Milliseconds(),
		m.ConfirmationThreshold, m.DegradedLatency.Milliseconds(),
		config, m.ParentID,
		enabled, tags, toMillis(now), nullableToken(m.PushToken), m.GroupID, m.ID,
		m.GroupID, m.GroupID,
	)
	if err != nil {
		return translateWriteErr(err)
	}
	return requireAffected(res, fmt.Sprintf("monitor %d ou o grupo dele", m.ID))
}

// FindByPushToken devolve o monitor dono do token de push.
func (r *monitorRepo) FindByPushToken(ctx context.Context, token string) (domain.Monitor, error) {
	if token == "" {
		return domain.Monitor{}, fmt.Errorf("token de push vazio: %w", store.ErrNotFound)
	}
	row := r.db.QueryRowContext(ctx, `SELECT`+monitorColumns+` FROM monitor WHERE push_token = ?`, token)

	m, err := scanMonitor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Monitor{}, fmt.Errorf("token de push: %w", store.ErrNotFound)
	}
	if err != nil {
		return domain.Monitor{}, err
	}
	return m, nil
}

// nullableToken grava NULL para o monitor sem token. Uma string vazia
// contaria como valor no índice único, e só um monitor sem token
// poderia existir.
func nullableToken(token string) any {
	if token == "" {
		return nil
	}
	return token
}

// Delete remove o monitor. Heartbeats e rollups saem em cascata pelo
// schema, desde que a checagem de chave estrangeira esteja ligada.
func (r *monitorRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM monitor WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando monitor %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("monitor %d", id))
}

// List pagina por keyset sobre o id. Keyset em vez de OFFSET porque as
// páginas continuam estáveis mesmo com inserções durante a navegação.
func (r *monitorRepo) List(ctx context.Context, f store.MonitorFilter) (store.Page[domain.Monitor], error) {
	f.Page = f.Page.Normalize()

	var (
		conds = []string{"id > ?"}
		args  = []any{f.Page.AfterID}
	)
	if f.Enabled != nil {
		conds = append(conds, "enabled = ?")
		args = append(args, boolToInt(*f.Enabled))
	}
	if f.Tag != "" {
		// tags é um array JSON; a busca por substring com aspas em volta
		// evita casar "prod" dentro de "producao".
		//
		// A etiqueta é escapada porque vai dentro de um padrão de LIKE, e
		// ali "%" e "_" são curingas. Sem isto, filtrar por "a_b" traria
		// também "axb", e por "100%" traria tudo que começa com 100 — a
		// listagem simplesmente vem demais, sem erro nenhum aparecer.
		conds = append(conds, `tags LIKE ? ESCAPE '\'`)
		args = append(args, "%\""+escapeLike(f.Tag)+"\"%")
	}
	if cond, condArgs := groupCondition("group_id", f.Groups); cond != "" {
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}

	// Pede um a mais que o limite para saber se há próxima página sem
	// precisar de um COUNT separado.
	args = append(args, f.Page.Limit+1)
	q := `SELECT` + monitorColumns + ` FROM monitor WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY id LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return store.Page[domain.Monitor]{}, fmt.Errorf("sqlstore: listando monitores: %w", err)
	}
	defer rows.Close()

	var items []domain.Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return store.Page[domain.Monitor]{}, err
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return store.Page[domain.Monitor]{}, fmt.Errorf("sqlstore: lendo monitores: %w", err)
	}

	page := store.Page[domain.Monitor]{Items: items}
	if len(items) > f.Page.Limit {
		page.Items = items[:f.Page.Limit]
		page.HasMore = true
	}
	return page, nil
}

// ---------- auxiliares ----------

// scanner cobre *sql.Row e *sql.Rows, que só compartilham Scan.
type scanner interface{ Scan(dest ...any) error }

func scanMonitor(sc scanner) (domain.Monitor, error) {
	var (
		m          domain.Monitor
		typeName   string
		intervalMS int64
		timeoutMS  int64
		degradedMS int64
		configJSON string
		parentID   sql.NullInt64
		enabled    int64
		tagsJSON   string
		createdMS  int64
		updatedMS  int64
		rearmedMS  int64
		pushToken  sql.NullString
		groupID    sql.NullInt64
	)

	err := sc.Scan(
		&m.ID, &m.Name, &typeName, &m.Target, &intervalMS, &timeoutMS,
		&m.ConfirmationThreshold, &degradedMS, &configJSON, &parentID,
		&enabled, &tagsJSON, &createdMS, &updatedMS, &rearmedMS, &pushToken, &groupID,
	)
	if err != nil {
		return domain.Monitor{}, err
	}
	m.Config = json.RawMessage(configJSON)

	typ, err := domain.ParseMonitorType(typeName)
	if err != nil {
		return domain.Monitor{}, fmt.Errorf("sqlstore: monitor %d: %w", m.ID, err)
	}
	m.Type = typ
	m.Interval = time.Duration(intervalMS) * time.Millisecond
	m.Timeout = time.Duration(timeoutMS) * time.Millisecond
	m.DegradedLatency = time.Duration(degradedMS) * time.Millisecond
	m.Enabled = enabled != 0
	m.CreatedAt = fromMillis(createdMS)
	m.UpdatedAt = fromMillis(updatedMS)
	m.RearmedAt = fromMillis(rearmedMS)
	m.PushToken = pushToken.String

	if parentID.Valid {
		id := parentID.Int64
		m.ParentID = &id
	}
	if groupID.Valid {
		id := groupID.Int64
		m.GroupID = &id
	}
	if m.Tags, err = decodeTags(tagsJSON); err != nil {
		return domain.Monitor{}, fmt.Errorf("sqlstore: monitor %d: %w", m.ID, err)
	}
	return m, nil
}

func encodeTags(tags []string) (string, error) {
	if len(tags) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("sqlstore: serializando tags: %w", err)
	}
	return string(b), nil
}

func decodeTags(s string) ([]string, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo tags: %w", err)
	}
	return tags, nil
}

// requireAffected transforma "nenhuma linha alterada" em ErrNotFound, para
// que a API distinga 404 de 500.
func requireAffected(res sql.Result, subject string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlstore: contando linhas afetadas: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", subject, store.ErrNotFound)
	}
	return nil
}

// groupExists é a condição de gravar um monitor: sem grupo, ou num grupo
// que existe. Recebe o grupo duas vezes. O CAST é para o PostgreSQL, que
// não deduz o tipo de um parâmetro que só aparece em "? IS NULL".
const groupExists = `(CAST(? AS BIGINT) IS NULL OR EXISTS (SELECT 1 FROM monitor_group WHERE id = ?))`

// translateWriteErr converte violação de unicidade do driver em
// store.ErrConflict, e a da chave estrangeira — grupo ou monitor pai que
// deixou de existir no meio do pedido — em store.ErrNotFound, mantendo o
// erro do banco fora das camadas de cima.
func translateWriteErr(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key") {
		return fmt.Errorf("%w: %s", store.ErrConflict, err)
	}
	if isForeignKeyViolation(err) {
		return fmt.Errorf("grupo ou monitor pai: %w", store.ErrNotFound)
	}
	return fmt.Errorf("sqlstore: escrevendo monitor: %w", err)
}

// isForeignKeyViolation reconhece a recusa da chave estrangeira nos dois
// bancos: "FOREIGN KEY constraint failed" no SQLite e "violates foreign
// key constraint" no PostgreSQL.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint")
}

// escapeLike neutraliza os curingas de um padrão de LIKE.
//
// Vale para os dois dialetos: "%" e "_" são curingas em ambos, e a
// cláusula ESCAPE tem a mesma sintaxe. A barra invertida vem primeiro,
// senão o escape das outras seria escapado de novo.
func escapeLike(v string) string {
	v = strings.ReplaceAll(v, `\`, `\`)
	v = strings.ReplaceAll(v, "%", `\%`)
	return strings.ReplaceAll(v, "_", `\_`)
}
