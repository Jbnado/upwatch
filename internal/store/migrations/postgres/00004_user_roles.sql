-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
ALTER TABLE app_user ADD COLUMN role TEXT NOT NULL DEFAULT 'admin';

CREATE INDEX idx_app_user_role ON app_user (role);

-- +goose Down
DROP INDEX idx_app_user_role;
ALTER TABLE app_user DROP COLUMN role;
