-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE monitor ADD COLUMN reminder_interval_ms BIGINT;
ALTER TABLE monitor_group ADD COLUMN reminder_interval_ms BIGINT;

ALTER TABLE incident ADD COLUMN reminded_at BIGINT;
ALTER TABLE incident ADD COLUMN acknowledged_at BIGINT;
ALTER TABLE incident ADD COLUMN acknowledged_by TEXT;
ALTER TABLE incident ADD COLUMN ack_note TEXT;

-- +goose Down
ALTER TABLE incident DROP COLUMN ack_note;
ALTER TABLE incident DROP COLUMN acknowledged_by;
ALTER TABLE incident DROP COLUMN acknowledged_at;
ALTER TABLE incident DROP COLUMN reminded_at;
ALTER TABLE monitor_group DROP COLUMN reminder_interval_ms;
ALTER TABLE monitor DROP COLUMN reminder_interval_ms;
