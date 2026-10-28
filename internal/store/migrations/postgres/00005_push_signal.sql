-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE push_state ADD COLUMN status     TEXT   NOT NULL DEFAULT 'up';
ALTER TABLE push_state ADD COLUMN message    TEXT   NOT NULL DEFAULT '';
ALTER TABLE push_state ADD COLUMN latency_ms BIGINT NOT NULL DEFAULT 0;
ALTER TABLE push_state ADD COLUMN signal_id    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE push_state ADD COLUMN evaluated_id BIGINT NOT NULL DEFAULT 0;

ALTER TABLE monitor ADD COLUMN rearmed_at BIGINT NOT NULL DEFAULT 0;
UPDATE monitor SET rearmed_at = created_at;

-- +goose Down
ALTER TABLE monitor DROP COLUMN rearmed_at;
ALTER TABLE push_state DROP COLUMN evaluated_id;
ALTER TABLE push_state DROP COLUMN signal_id;
ALTER TABLE push_state DROP COLUMN latency_ms;
ALTER TABLE push_state DROP COLUMN message;
ALTER TABLE push_state DROP COLUMN status;
