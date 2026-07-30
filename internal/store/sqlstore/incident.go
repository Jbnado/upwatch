package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// ---------- estado do monitor ----------

type stateRepo struct{ db *db }

func (r *stateRepo) Get(ctx context.Context, monitorID int64) (domain.MonitorState, error) {
	var (
		statusName    string
		candidateName string
		consecutive   int
		sinceMS       int64
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT status, candidate, consecutive, since FROM monitor_state WHERE monitor_id = ?`,
		monitorID).Scan(&statusName, &candidateName, &consecutive, &sinceMS)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Monitor ainda não verificado: o zero é o começo legítimo, não
		// um erro que o chamador precise tratar.
		return domain.MonitorState{}, nil
	case err != nil:
		return domain.MonitorState{}, fmt.Errorf("sqlstore: lendo estado do monitor %d: %w", monitorID, err)
	}

	return decodeState(statusName, candidateName, consecutive, sinceMS)
}

func (r *stateRepo) Save(ctx context.Context, monitorID int64, s domain.MonitorState) error {
	const q = `
		INSERT INTO monitor_state (monitor_id, status, candidate, consecutive, since)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (monitor_id) DO UPDATE SET
			status      = excluded.status,
			candidate   = excluded.candidate,
			consecutive = excluded.consecutive,
			since       = excluded.since`

	_, err := r.db.ExecContext(ctx, q,
		monitorID, s.Status.String(), s.Candidate.String(), s.Consecutive, toMillis(s.Since))
	if err != nil {
		return fmt.Errorf("sqlstore: gravando estado do monitor %d: %w", monitorID, err)
	}
	return nil
}

func (r *stateRepo) All(ctx context.Context) (map[int64]domain.MonitorState, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT monitor_id, status, candidate, consecutive, since FROM monitor_state`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: carregando estados: %w", err)
	}
	defer rows.Close()

	out := map[int64]domain.MonitorState{}
	for rows.Next() {
		var (
			id                        int64
			statusName, candidateName string
			consecutive               int
			sinceMS                   int64
		)
		if err := rows.Scan(&id, &statusName, &candidateName, &consecutive, &sinceMS); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo estado: %w", err)
		}

		s, err := decodeState(statusName, candidateName, consecutive, sinceMS)
		if err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

func decodeState(statusName, candidateName string, consecutive int, sinceMS int64) (domain.MonitorState, error) {
	status, err := domain.ParseStatus(statusName)
	if err != nil {
		return domain.MonitorState{}, fmt.Errorf("sqlstore: estado inválido: %w", err)
	}
	candidate, err := domain.ParseStatus(candidateName)
	if err != nil {
		return domain.MonitorState{}, fmt.Errorf("sqlstore: candidato inválido: %w", err)
	}

	return domain.MonitorState{
		Status: status, Candidate: candidate,
		Consecutive: consecutive, Since: fromMillis(sinceMS),
	}, nil
}

// ---------- incidentes ----------

type incidentRepo struct{ db *db }

const incidentColumns = `id, monitor_id, started_at, resolved_at, cause`

func (r *incidentRepo) Open(ctx context.Context, i *domain.Incident) error {
	id, err := r.db.insertID(ctx, `
		INSERT INTO incident (monitor_id, started_at, cause) VALUES (?, ?, ?)`,
		i.MonitorID, toMillis(i.StartedAt), i.Cause)
	if err != nil {
		// O índice parcial garante uma queda aberta por monitor; a
		// violação vira conflito para o chamador distinguir de falha real.
		return translateAuthErr(err, "incidente")
	}
	i.ID = id
	return nil
}

// Resolve encerra a queda aberta do monitor.
//
// Silencioso quando não há nenhuma: encerrar o que já acabou não é erro, e
// tratar como tal obrigaria o motor a consultar antes de agir.
func (r *incidentRepo) Resolve(ctx context.Context, monitorID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE incident SET resolved_at = ?
		WHERE monitor_id = ? AND resolved_at IS NULL`,
		toMillis(at), monitorID)
	if err != nil {
		return fmt.Errorf("sqlstore: encerrando incidente do monitor %d: %w", monitorID, err)
	}
	return nil
}

func (r *incidentRepo) Current(ctx context.Context, monitorID int64) (domain.Incident, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM incident WHERE monitor_id = ? AND resolved_at IS NULL`,
		monitorID)

	i, err := scanIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Incident{}, fmt.Errorf("incidente do monitor %d: %w", monitorID, store.ErrNotFound)
	}
	return i, err
}

func (r *incidentRepo) List(ctx context.Context, f store.IncidentFilter) (store.Page[domain.Incident], error) {
	f.Page = f.Page.Normalize()

	conds := []string{"id > ?"}
	args := []any{f.Page.AfterID}

	if f.MonitorID > 0 {
		conds = append(conds, "monitor_id = ?")
		args = append(args, f.MonitorID)
	}
	if f.OnlyOpen {
		conds = append(conds, "resolved_at IS NULL")
	}
	args = append(args, f.Page.Limit+1)

	q := `SELECT ` + incidentColumns + ` FROM incident WHERE ` +
		joinAnd(conds) + ` ORDER BY id DESC LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return store.Page[domain.Incident]{}, fmt.Errorf("sqlstore: listando incidentes: %w", err)
	}
	defer rows.Close()

	var items []domain.Incident
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return store.Page[domain.Incident]{}, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return store.Page[domain.Incident]{}, fmt.Errorf("sqlstore: lendo incidentes: %w", err)
	}

	page := store.Page[domain.Incident]{Items: items}
	if len(items) > f.Page.Limit {
		page.Items = items[:f.Page.Limit]
		page.HasMore = true
	}
	return page, nil
}

func scanIncident(sc scanner) (domain.Incident, error) {
	var (
		i          domain.Incident
		startedMS  int64
		resolvedMS sql.NullInt64
	)
	if err := sc.Scan(&i.ID, &i.MonitorID, &startedMS, &resolvedMS, &i.Cause); err != nil {
		return domain.Incident{}, err
	}

	i.StartedAt = fromMillis(startedMS)
	if resolvedMS.Valid {
		at := fromMillis(resolvedMS.Int64)
		i.ResolvedAt = &at
	}
	return i, nil
}

// ---------- canais ----------

type channelRepo struct{ db *db }

const channelColumns = `id, name, type, config, enabled, created_at, updated_at`

func (r *channelRepo) Create(ctx context.Context, c *domain.Channel) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	c.CreatedAt, c.UpdatedAt = now, now

	id, err := r.db.insertID(ctx, `
		INSERT INTO notification_channel (name, type, config, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.Name, c.Type, encodeConfig(c.Config), boolToInt(c.Enabled), toMillis(now), toMillis(now))
	if err != nil {
		return translateAuthErr(err, "canal")
	}
	c.ID = id
	return nil
}

func (r *channelRepo) Get(ctx context.Context, id int64) (domain.Channel, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM notification_channel WHERE id = ?`, id)

	c, err := scanChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Channel{}, fmt.Errorf("canal %d: %w", id, store.ErrNotFound)
	}
	return c, err
}

func (r *channelRepo) Update(ctx context.Context, c domain.Channel) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	res, err := r.db.ExecContext(ctx, `
		UPDATE notification_channel
		SET name = ?, type = ?, config = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		c.Name, c.Type, encodeConfig(c.Config), boolToInt(c.Enabled), toMillis(now), c.ID)
	if err != nil {
		return translateAuthErr(err, "canal")
	}
	return requireAffected(res, fmt.Sprintf("canal %d", c.ID))
}

func (r *channelRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM notification_channel WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando canal %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("canal %d", id))
}

func (r *channelRepo) List(ctx context.Context) ([]domain.Channel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM notification_channel ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando canais: %w", err)
	}
	defer rows.Close()

	return collectChannels(rows)
}

func (r *channelRepo) Link(ctx context.Context, monitorID, channelID int64) error {
	// Vincular duas vezes é idempotente: a interface pode reenviar o
	// conjunto inteiro sem precisar calcular a diferença.
	const q = `
		INSERT INTO monitor_channel (monitor_id, channel_id) VALUES (?, ?)
		ON CONFLICT (monitor_id, channel_id) DO NOTHING`

	if _, err := r.db.ExecContext(ctx, q, monitorID, channelID); err != nil {
		return fmt.Errorf("sqlstore: vinculando canal %d ao monitor %d: %w", channelID, monitorID, err)
	}
	return nil
}

func (r *channelRepo) Unlink(ctx context.Context, monitorID, channelID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM monitor_channel WHERE monitor_id = ? AND channel_id = ?`,
		monitorID, channelID)
	if err != nil {
		return fmt.Errorf("sqlstore: desvinculando canal %d do monitor %d: %w", channelID, monitorID, err)
	}
	return nil
}

// ForMonitor devolve os canais habilitados do monitor.
//
// O filtro por habilitado vive aqui para nenhum chamador precisar lembrar
// dele: um canal desligado que ainda recebesse avisos tornaria o botão de
// desligar decorativo.
func (r *channelRepo) ForMonitor(ctx context.Context, monitorID int64) ([]domain.Channel, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.type, c.config, c.enabled, c.created_at, c.updated_at
		FROM notification_channel c
		JOIN monitor_channel mc ON mc.channel_id = c.id
		WHERE mc.monitor_id = ? AND c.enabled = 1
		ORDER BY c.id`, monitorID)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando canais do monitor %d: %w", monitorID, err)
	}
	defer rows.Close()

	return collectChannels(rows)
}

func collectChannels(rows *sql.Rows) ([]domain.Channel, error) {
	var out []domain.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo canais: %w", err)
	}
	return out, nil
}

func scanChannel(sc scanner) (domain.Channel, error) {
	var (
		c          domain.Channel
		configJSON string
		enabled    int64
		createdMS  int64
		updatedMS  int64
	)
	err := sc.Scan(&c.ID, &c.Name, &c.Type, &configJSON, &enabled, &createdMS, &updatedMS)
	if err != nil {
		return domain.Channel{}, err
	}

	c.Config = json.RawMessage(configJSON)
	c.Enabled = enabled != 0
	c.CreatedAt = fromMillis(createdMS)
	c.UpdatedAt = fromMillis(updatedMS)
	return c, nil
}

func joinAnd(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}
