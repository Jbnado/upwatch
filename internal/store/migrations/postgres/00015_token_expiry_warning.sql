-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE api_token ADD COLUMN expiry_warned_days INTEGER;

-- +goose Down
ALTER TABLE api_token DROP COLUMN expiry_warned_days;
