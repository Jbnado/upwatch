package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bernardojoao/upwatch/internal/domain"
	"github.com/bernardojoao/upwatch/internal/store"
)

// WriteHeartbeats grava o lote numa única transação.
//
// Uma transação por lote em vez de uma por batida é a decisão de
// desempenho central do projeto: com mil monitores a cada 30 segundos são
// ~33 escritas por segundo, que viram cerca de uma transação por segundo.
// Gravar individualmente pagaria um fsync por batida e travaria o SQLite.
func (s *Store) WriteHeartbeats(ctx context.Context, hbs []domain.Heartbeat) error {
	if len(hbs) == 0 {
		return nil
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		const q = `
			INSERT INTO heartbeat (monitor_id, probe_id, ts, status, latency_ms, message)
			VALUES (?, ?, ?, ?, ?, ?)`

		// Um statement preparado e reutilizado evita reparse por linha e
		// contorna o teto de variáveis por comando em lotes grandes.
		stmt, err := tx.PrepareContext(ctx, q)
		if err != nil {
			return fmt.Errorf("sqlstore: preparando inserção de heartbeat: %w", err)
		}
		defer stmt.Close()

		for _, hb := range hbs {
			hb = hb.Normalize()
			if _, err := stmt.ExecContext(ctx,
				hb.MonitorID, hb.ProbeID, toMillis(hb.Timestamp),
				hb.Status.String(), hb.LatencyMS, hb.Message,
			); err != nil {
				return fmt.Errorf("sqlstore: gravando heartbeat do monitor %d: %w", hb.MonitorID, err)
			}
		}
		return nil
	})
}

// QueryHeartbeats devolve batidas da janela em ordem cronológica.
//
// A ordenação é garantida porque a agregação percorre a janela em
// sequência; deixá-la a cargo do plano de execução tornaria os percentis
// dependentes do banco.
func (s *Store) QueryHeartbeats(ctx context.Context, q store.HeartbeatQuery) ([]domain.Heartbeat, error) {
	q = q.Normalize()

	args := []any{q.MonitorID, toMillis(q.Range.From), toMillis(q.Range.To)}
	// O índice composto (monitor_id, ts) atende exatamente este predicado.
	// Sem ele a consulta degrada para varredura completa da maior tabela
	// do banco.
	sqlText := `
		SELECT monitor_id, probe_id, ts, status, latency_ms, message
		FROM heartbeat
		WHERE monitor_id = ? AND ts >= ? AND ts < ?`
	if q.ProbeID != "" {
		sqlText += ` AND probe_id = ?`
		args = append(args, q.ProbeID)
	}
	sqlText += ` ORDER BY ts LIMIT ?`
	args = append(args, q.Limit)

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: consultando heartbeats: %w", err)
	}
	defer rows.Close()

	var out []domain.Heartbeat
	for rows.Next() {
		var (
			hb         domain.Heartbeat
			ts         int64
			statusName string
		)
		if err := rows.Scan(&hb.MonitorID, &hb.ProbeID, &ts, &statusName, &hb.LatencyMS, &hb.Message); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo heartbeat: %w", err)
		}
		status, err := domain.ParseStatus(statusName)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: heartbeat do monitor %d: %w", hb.MonitorID, err)
		}
		hb.Status = status
		hb.Timestamp = fromMillis(ts)
		out = append(out, hb)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo heartbeats: %w", err)
	}
	return out, nil
}

// WriteRollups grava agregados de forma idempotente.
//
// Reescrever um bucket substitui a linha em vez de criar outra: uma
// reexecução após falha do worker precisa ser segura, e sem isso os
// contadores inflariam a cada tentativa.
func (s *Store) WriteRollups(ctx context.Context, rs []domain.Rollup) error {
	if len(rs) == 0 {
		return nil
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		const q = `
			INSERT INTO rollup (
				monitor_id, probe_id, resolution, bucket_start,
				total, up, down, degraded,
				latency_samples, latency_avg_ms, latency_min_ms, latency_max_ms,
				latency_p50_ms, latency_p95_ms, latency_p99_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (monitor_id, probe_id, resolution, bucket_start) DO UPDATE SET
				total           = excluded.total,
				up              = excluded.up,
				down            = excluded.down,
				degraded        = excluded.degraded,
				latency_samples = excluded.latency_samples,
				latency_avg_ms  = excluded.latency_avg_ms,
				latency_min_ms  = excluded.latency_min_ms,
				latency_max_ms  = excluded.latency_max_ms,
				latency_p50_ms  = excluded.latency_p50_ms,
				latency_p95_ms  = excluded.latency_p95_ms,
				latency_p99_ms  = excluded.latency_p99_ms`

		stmt, err := tx.PrepareContext(ctx, q)
		if err != nil {
			return fmt.Errorf("sqlstore: preparando gravação de rollup: %w", err)
		}
		defer stmt.Close()

		for _, r := range rs {
			probeID := r.ProbeID
			if probeID == "" {
				probeID = domain.DefaultProbeID
			}
			if _, err := stmt.ExecContext(ctx,
				r.MonitorID, probeID, r.Resolution.String(), toMillis(r.BucketStart),
				r.Total, r.Up, r.Down, r.Degraded,
				r.LatencySamples, r.LatencyAvgMS, r.LatencyMinMS, r.LatencyMaxMS,
				r.LatencyP50MS, r.LatencyP95MS, r.LatencyP99MS,
			); err != nil {
				return fmt.Errorf("sqlstore: gravando rollup do monitor %d: %w", r.MonitorID, err)
			}
		}
		return nil
	})
}

// QueryRollups devolve agregados da janela em ordem cronológica.
func (s *Store) QueryRollups(ctx context.Context, q store.RollupQuery) ([]domain.Rollup, error) {
	q = q.Normalize()

	args := []any{q.MonitorID, q.Resolution.String(), toMillis(q.Range.From), toMillis(q.Range.To)}
	sqlText := `
		SELECT monitor_id, probe_id, resolution, bucket_start,
		       total, up, down, degraded,
		       latency_samples, latency_avg_ms, latency_min_ms, latency_max_ms,
		       latency_p50_ms, latency_p95_ms, latency_p99_ms
		FROM rollup
		WHERE monitor_id = ? AND resolution = ? AND bucket_start >= ? AND bucket_start < ?`
	if q.ProbeID != "" {
		sqlText += ` AND probe_id = ?`
		args = append(args, q.ProbeID)
	}
	sqlText += ` ORDER BY bucket_start`

	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: consultando rollups: %w", err)
	}
	defer rows.Close()

	var out []domain.Rollup
	for rows.Next() {
		var (
			r       domain.Rollup
			resName string
			bucket  int64
		)
		if err := rows.Scan(
			&r.MonitorID, &r.ProbeID, &resName, &bucket,
			&r.Total, &r.Up, &r.Down, &r.Degraded,
			&r.LatencySamples, &r.LatencyAvgMS, &r.LatencyMinMS, &r.LatencyMaxMS,
			&r.LatencyP50MS, &r.LatencyP95MS, &r.LatencyP99MS,
		); err != nil {
			return nil, fmt.Errorf("sqlstore: lendo rollup: %w", err)
		}
		res, err := domain.ParseResolution(resName)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: rollup do monitor %d: %w", r.MonitorID, err)
		}
		r.Resolution = res
		r.BucketStart = fromMillis(bucket)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: lendo rollups: %w", err)
	}
	return out, nil
}

// PruneHeartbeats apaga batidas estritamente anteriores a before.
//
// O corte é exclusivo para que a batida exatamente na fronteira sobreviva,
// evitando perder a primeira amostra do período que ainda será agregado.
func (s *Store) PruneHeartbeats(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM heartbeat WHERE ts < ?`, toMillis(before))
	if err != nil {
		return 0, fmt.Errorf("sqlstore: podando heartbeats: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando heartbeats podados: %w", err)
	}
	return n, nil
}

// PruneRollups apaga agregados de uma resolução anteriores a before.
//
// A resolução é obrigatória porque cada camada tem retenção própria: podar
// a horária não pode levar junto a diária, que sustenta o gráfico de meses.
func (s *Store) PruneRollups(ctx context.Context, res domain.Resolution, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM rollup WHERE resolution = ? AND bucket_start < ?`,
		res.String(), toMillis(before))
	if err != nil {
		return 0, fmt.Errorf("sqlstore: podando rollups %s: %w", res, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: contando rollups podados: %w", err)
	}
	return n, nil
}

// RollupWatermark devolve o último bucket já agregado na resolução.
// Devolve o tempo zero quando nada foi processado ainda.
func (s *Store) RollupWatermark(ctx context.Context, res domain.Resolution) (time.Time, error) {
	var ms int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_bucket FROM rollup_state WHERE resolution = ?`, res.String()).Scan(&ms)

	switch {
	case err == sql.ErrNoRows:
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, fmt.Errorf("sqlstore: lendo marca d'água %s: %w", res, err)
	}
	return fromMillis(ms), nil
}

// SetRollupWatermark avança a marca d'água da resolução.
func (s *Store) SetRollupWatermark(ctx context.Context, res domain.Resolution, bucket time.Time) error {
	const q = `
		INSERT INTO rollup_state (resolution, last_bucket) VALUES (?, ?)
		ON CONFLICT (resolution) DO UPDATE SET last_bucket = excluded.last_bucket`

	if _, err := s.db.ExecContext(ctx, q, res.String(), toMillis(bucket)); err != nil {
		return fmt.Errorf("sqlstore: gravando marca d'água %s: %w", res, err)
	}
	return nil
}
