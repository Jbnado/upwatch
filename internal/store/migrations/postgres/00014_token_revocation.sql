-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE api_token ADD COLUMN revoked_at BIGINT;
ALTER TABLE api_token ADD COLUMN revoked_reason TEXT;
ALTER TABLE api_token ADD COLUMN revoked_by BIGINT REFERENCES app_user (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE api_token DROP COLUMN revoked_by;
ALTER TABLE api_token DROP COLUMN revoked_reason;
ALTER TABLE api_token DROP COLUMN revoked_at;
