-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite,
-- para não divergirem em duas cópias.
ALTER TABLE status_page ADD COLUMN is_default INTEGER;

CREATE UNIQUE INDEX idx_status_page_default ON status_page (is_default);

-- +goose Down
DROP INDEX idx_status_page_default;
ALTER TABLE status_page DROP COLUMN is_default;
