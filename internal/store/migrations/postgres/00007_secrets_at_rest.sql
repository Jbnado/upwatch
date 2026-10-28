-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
CREATE TABLE secret_key_check (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value TEXT    NOT NULL
);

-- +goose Down
DROP TABLE secret_key_check;
