package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Jbnado/upwatch/internal/domain"
	"github.com/Jbnado/upwatch/internal/store"
)

// ---------- janelas de manutenção ----------

type maintenanceRepo struct{ db *db }

// Maintenance devolve o repositório de janelas de manutenção.
func (s *Store) Maintenance() store.MaintenanceRepo { return &maintenanceRepo{db: s.db} }

const maintenanceColumns = `id, title, message, starts_at, ends_at, recurrence, time_zone, ended_at, created_at, updated_at`

func (r *maintenanceRepo) Create(ctx context.Context, w *domain.MaintenanceWindow) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	w.CreatedAt, w.UpdatedAt = now, now

	return r.db.inTx(ctx, func(t *tx) error {
		id, err := t.insertID(ctx, `
			INSERT INTO maintenance_window
				(title, message, starts_at, ends_at, recurrence, time_zone, ended_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.Title, w.Message, toMillis(w.StartsAt), toMillis(w.EndsAt), string(w.Recurrence),
			w.TimeZone, nullableMillis(w.EndedAt), toMillis(now), toMillis(now))
		if err != nil {
			return fmt.Errorf("sqlstore: criando janela de manutenção: %w", err)
		}
		w.ID = id
		return replaceMaintenanceTargets(ctx, t, *w)
	})
}

func (r *maintenanceRepo) Get(ctx context.Context, id int64) (domain.MaintenanceWindow, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+maintenanceColumns+` FROM maintenance_window WHERE id = ?`, id)
	w, err := scanMaintenance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MaintenanceWindow{}, fmt.Errorf("janela de manutenção %d: %w", id, store.ErrNotFound)
	}
	if err != nil {
		return domain.MaintenanceWindow{}, err
	}

	alvos, err := r.targets(ctx, &id)
	if err != nil {
		return domain.MaintenanceWindow{}, err
	}
	a := alvos[id]
	w.MonitorIDs, w.GroupIDs = a.monitors, a.groups
	return w, nil
}

func (r *maintenanceRepo) Update(ctx context.Context, w domain.MaintenanceWindow) error {
	now := time.Now().UTC().Truncate(time.Millisecond)

	return r.db.inTx(ctx, func(t *tx) error {
		res, err := t.ExecContext(ctx, `
			UPDATE maintenance_window
			SET title = ?, message = ?, starts_at = ?, ends_at = ?, recurrence = ?, time_zone = ?, updated_at = ?
			WHERE id = ?`,
			w.Title, w.Message, toMillis(w.StartsAt), toMillis(w.EndsAt), string(w.Recurrence),
			w.TimeZone, toMillis(now), w.ID)
		if err != nil {
			return fmt.Errorf("sqlstore: atualizando janela de manutenção %d: %w", w.ID, err)
		}
		if err := requireAffected(res, fmt.Sprintf("janela de manutenção %d", w.ID)); err != nil {
			return err
		}
		return replaceMaintenanceTargets(ctx, t, w)
	})
}

func (r *maintenanceRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM maintenance_window WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlstore: apagando janela de manutenção %d: %w", id, err)
	}
	return requireAffected(res, fmt.Sprintf("janela de manutenção %d", id))
}

// End leva a condição na própria instrução: dois encerramentos ao mesmo
// tempo ficam com o mais cedo, sem ler antes de escrever.
func (r *maintenanceRepo) End(ctx context.Context, id int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE maintenance_window SET ended_at = ?, updated_at = ?
		WHERE id = ? AND (ended_at IS NULL OR ended_at > ?)`,
		toMillis(at), toMillis(time.Now().UTC()), id, toMillis(at))
	if err != nil {
		return fmt.Errorf("sqlstore: encerrando janela de manutenção %d: %w", id, err)
	}
	// Nada mudou tanto para a janela já encerrada antes quanto para a que
	// não existe; só a segunda é erro.
	_, err = r.Get(ctx, id)
	return err
}

func (r *maintenanceRepo) List(ctx context.Context) ([]domain.MaintenanceWindow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+maintenanceColumns+` FROM maintenance_window ORDER BY starts_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: listando janelas de manutenção: %w", err)
	}
	defer rows.Close()

	out := []domain.MaintenanceWindow{}
	for rows.Next() {
		w, err := scanMaintenance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo janelas de manutenção: %w", err)
	}

	alvos, err := r.targets(ctx, nil)
	if err != nil {
		return nil, err
	}
	for i := range out {
		a := alvos[out[i].ID]
		out[i].MonitorIDs, out[i].GroupIDs = a.monitors, a.groups
	}
	return out, nil
}

type maintenanceTargets struct{ monitors, groups []int64 }

// targets lê os alvos de uma janela, ou de todas com id nulo. Duas
// consultas para a lista inteira, e não duas por janela: o motor relê a
// lista periodicamente.
func (r *maintenanceRepo) targets(ctx context.Context, id *int64) (map[int64]maintenanceTargets, error) {
	out := map[int64]maintenanceTargets{}
	ler := func(tabela, coluna string, add func(a *maintenanceTargets, v int64)) error {
		q := `SELECT window_id, ` + coluna + ` FROM ` + tabela
		args := []any{}
		if id != nil {
			q += ` WHERE window_id = ?`
			args = append(args, *id)
		}
		q += ` ORDER BY window_id, ` + coluna
		rows, err := r.db.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("sqlstore: listando alvos de manutenção: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var janela, alvo int64
			if err := rows.Scan(&janela, &alvo); err != nil {
				return fmt.Errorf("sqlstore: lendo alvo de manutenção: %w", err)
			}
			a := out[janela]
			add(&a, alvo)
			out[janela] = a
		}
		return rows.Err()
	}

	if err := ler("maintenance_monitor", "monitor_id", func(a *maintenanceTargets, v int64) {
		a.monitors = append(a.monitors, v)
	}); err != nil {
		return nil, err
	}
	if err := ler("maintenance_group", "group_id", func(a *maintenanceTargets, v int64) {
		a.groups = append(a.groups, v)
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// replaceMaintenanceTargets troca o conjunto inteiro dentro da transação.
// Monitor ou grupo inexistente é ErrNotFound, pela chave estrangeira.
func replaceMaintenanceTargets(ctx context.Context, t *tx, w domain.MaintenanceWindow) error {
	for _, tabela := range []string{"maintenance_monitor", "maintenance_group"} {
		if _, err := t.ExecContext(ctx, `DELETE FROM `+tabela+` WHERE window_id = ?`, w.ID); err != nil {
			return fmt.Errorf("sqlstore: limpando alvos da janela %d: %w", w.ID, err)
		}
	}
	// Id repetido no pedido é o mesmo alvo duas vezes, não um conflito.
	for _, id := range distinct(w.MonitorIDs) {
		if _, err := t.ExecContext(ctx,
			`INSERT INTO maintenance_monitor (window_id, monitor_id) VALUES (?, ?)`, w.ID, id); err != nil {
			return translateMaintenanceErr(err, "monitor", id)
		}
	}
	for _, id := range distinct(w.GroupIDs) {
		if _, err := t.ExecContext(ctx,
			`INSERT INTO maintenance_group (window_id, group_id) VALUES (?, ?)`, w.ID, id); err != nil {
			return translateMaintenanceErr(err, "grupo", id)
		}
	}
	return nil
}

func distinct(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

func translateMaintenanceErr(err error, alvo string, id int64) error {
	if isForeignKeyViolation(err) {
		return fmt.Errorf("%s %d: %w", alvo, id, store.ErrNotFound)
	}
	return fmt.Errorf("sqlstore: gravando alvo da janela: %w", err)
}

func scanMaintenance(sc scanner) (domain.MaintenanceWindow, error) {
	var (
		w                    domain.MaintenanceWindow
		rec                  string
		startsMS, endsMS     int64
		endedMS              sql.NullInt64
		createdMS, updatedMS int64
	)
	if err := sc.Scan(&w.ID, &w.Title, &w.Message, &startsMS, &endsMS, &rec, &w.TimeZone,
		&endedMS, &createdMS, &updatedMS); err != nil {
		return domain.MaintenanceWindow{}, err
	}
	w.StartsAt, w.EndsAt = fromMillis(startsMS), fromMillis(endsMS)
	w.Recurrence = domain.Recurrence(rec)
	if endedMS.Valid {
		at := fromMillis(endedMS.Int64)
		w.EndedAt = &at
	}
	w.CreatedAt, w.UpdatedAt = fromMillis(createdMS), fromMillis(updatedMS)
	return w, nil
}
