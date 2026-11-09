-- +goose Up

-- Equivalente Postgres. As razões estão comentadas no arquivo SQLite.
CREATE TABLE user_totp (
    user_id    BIGINT  NOT NULL PRIMARY KEY REFERENCES app_user (id) ON DELETE CASCADE,
    secret     TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 0,
    last_step  BIGINT  NOT NULL DEFAULT 0,
    created_at BIGINT  NOT NULL,
    enabled_at BIGINT
);

CREATE TABLE user_recovery_code (
    user_id   BIGINT NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    code_hash TEXT   NOT NULL,
    used_at   BIGINT,
    PRIMARY KEY (user_id, code_hash)
);

CREATE TABLE login_challenge (
    token_hash TEXT    NOT NULL PRIMARY KEY,
    user_id    BIGINT  NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at BIGINT  NOT NULL,
    expires_at BIGINT  NOT NULL
);

CREATE INDEX idx_login_challenge_expires ON login_challenge (expires_at);

-- +goose Down
DROP INDEX idx_login_challenge_expires;
DROP TABLE login_challenge;
DROP TABLE user_recovery_code;
DROP TABLE user_totp;
