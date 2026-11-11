-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE api_token ADD COLUMN rotated_at BIGINT;
ALTER TABLE api_token ADD COLUMN predecessor_id BIGINT REFERENCES api_token (id) ON DELETE SET NULL;

CREATE INDEX idx_api_token_predecessor ON api_token (predecessor_id);

-- +goose Down
DROP INDEX idx_api_token_predecessor;
ALTER TABLE api_token DROP COLUMN predecessor_id;
ALTER TABLE api_token DROP COLUMN rotated_at;
