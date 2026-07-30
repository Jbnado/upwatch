package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

type monitorRepo struct {
	db *db
}

const monitorColumns = `
	id, name, type, target, interval_ms, timeout_ms,
	confirmation_threshold, degraded_latency_ms, config, parent_id,
	enabled, tags, created_at, updated_at`

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
	m.CreatedAt, m.UpdatedAt = now, now

	const q = `
		INSERT INTO monitor (
			name, type, target, interval_ms, timeout_ms,
			confirmation_threshold, degraded_latency_ms, config, parent_id,
			enabled, tags, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	id, err := r.db.insertID(ctx, q,
		m.Name, m.Type.String(), m.Target,
		m.Interval.Milliseconds(), m.Timeout.Milliseconds(),
		m.ConfirmationThreshold, m.DegradedLatency.Milliseconds(),
		encodeConfig(m.Config), m.ParentID,
		boolToInt(m.Enabled), tags, toMillis(now), toMillis(now),
	)
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
func (r *monitorRepo) Update(ctx context.Context, m domain.Monitor) error {
	tags, err := encodeTags(m.Tags)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Millisecond)

	const q = `
		UPDATE monitor SET
			name = ?, type = ?, target = ?, interval_ms = ?, timeout_ms = ?,
			confirmation_threshold = ?, degraded_latency_ms = ?, config = ?, parent_id = ?,
			enabled = ?, tags = ?, updated_at = ?
		WHERE id = ?`

	res, err := r.db.ExecContext(ctx, q,
		m.Name, m.Type.String(), m.Target,
		m.Interval.Milliseconds(), m.Timeout.Milliseconds(),
		m.ConfirmationThreshold, m.DegradedLatency.Milliseconds(),
		encodeConfig(m.Config), m.ParentID,
		boolToInt(m.Enabled), tags, toMillis(now), m.ID,
	)
	if err != nil {
		return translateWriteErr(err)
	}
	return requireAffected(res, fmt.Sprintf("monitor %d", m.ID))
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
		// tags é um array JSON; a busca por substring com aspas evita
		// casar "prod" dentro de "producao".
		conds = append(conds, "tags LIKE ?")
		args = append(args, "%\""+f.Tag+"\"%")
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
	)

	err := sc.Scan(
		&m.ID, &m.Name, &typeName, &m.Target, &intervalMS, &timeoutMS,
		&m.ConfirmationThreshold, &degradedMS, &configJSON, &parentID,
		&enabled, &tagsJSON, &createdMS, &updatedMS,
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

	if parentID.Valid {
		id := parentID.Int64
		m.ParentID = &id
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

// translateWriteErr converte violação de unicidade do driver em
// store.ErrConflict, mantendo o erro do banco fora das camadas de cima.
func translateWriteErr(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key") {
		return fmt.Errorf("%w: %s", store.ErrConflict, err)
	}
	return fmt.Errorf("sqlstore: escrevendo monitor: %w", err)
}
