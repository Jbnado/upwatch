-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE push_state ADD COLUMN status     TEXT   NOT NULL DEFAULT 'up';
ALTER TABLE push_state ADD COLUMN message    TEXT   NOT NULL DEFAULT '';
ALTER TABLE push_state ADD COLUMN latency_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE push_state ADD COLUMN signal_id    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE push_state ADD COLUMN evaluated_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE monitor ADD COLUMN rearmed_at BIGINT NOT NULL DEFAULT 0;
UPDATE monitor SET rearmed_at = created_at;

ALTER TABLE monitor ADD COLUMN push_token TEXT;
UPDATE monitor m SET push_token = m.config::jsonb ->> 'token'
WHERE m.type = 'push'
  AND jsonb_typeof(m.config::jsonb -> 'token') = 'string'
  AND m.config::jsonb ->> 'token' <> ''
  AND m.id = (
    SELECT MIN(o.id) FROM monitor o
    WHERE o.type = 'push'
      AND o.config::jsonb ->> 'token' = m.config::jsonb ->> 'token'
  );
CREATE UNIQUE INDEX idx_monitor_push_token ON monitor (push_token);

-- +goose Down
DROP INDEX idx_monitor_push_token;
ALTER TABLE monitor DROP COLUMN push_token;
ALTER TABLE monitor DROP COLUMN rearmed_at;
ALTER TABLE push_state DROP COLUMN evaluated_id;
ALTER TABLE push_state DROP COLUMN signal_id;
ALTER TABLE push_state DROP COLUMN latency_ms;
ALTER TABLE push_state DROP COLUMN message;
ALTER TABLE push_state DROP COLUMN status;
