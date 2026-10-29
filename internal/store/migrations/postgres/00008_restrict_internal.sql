-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE monitor ADD COLUMN restrict_internal INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE monitor DROP COLUMN restrict_internal;
