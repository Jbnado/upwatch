-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE notification_channel ADD COLUMN events TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE notification_channel DROP COLUMN events;
