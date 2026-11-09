-- +goose Up

-- Verificação em duas etapas (TOTP), opcional por conta.
--
-- Uma linha por conta, que nasce pendente no começo do cadastro e só
-- passa a valer depois que um código gerado pelo aplicativo é aceito.
-- Pendente e ligado moram na mesma linha para que recomeçar o cadastro
-- substitua o segredo anterior, em vez de acumular segredos esquecidos.
CREATE TABLE user_totp (
    user_id    INTEGER NOT NULL PRIMARY KEY REFERENCES app_user (id) ON DELETE CASCADE,
    -- Cifrado com a chave de segredos (ver 00007). Diferente da senha,
    -- não dá para guardar só um hash: conferir o código exige o segredo.
    secret     TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 0,
    -- Último passo de 30 s aceito. O código seguinte precisa ser de um
    -- passo posterior, senão quem viu o código na tela o reusaria dentro
    -- da mesma janela.
    last_step  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    enabled_at INTEGER
) WITHOUT ROWID;

-- Códigos de recuperação, de uso único. Só o hash: quem ler o banco não
-- ganha um segundo fator.
CREATE TABLE user_recovery_code (
    user_id   INTEGER NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    code_hash TEXT    NOT NULL,
    used_at   INTEGER,
    PRIMARY KEY (user_id, code_hash)
) WITHOUT ROWID;

-- Desafio entre a senha e o código. A senha certa ainda não abre sessão;
-- abre este desafio, que vale minutos, aceita poucas tentativas e serve
-- uma vez. Gravado, e não só em memória, para o segundo passo funcionar
-- quando cair em outra instância.
CREATE TABLE login_challenge (
    token_hash TEXT    NOT NULL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    attempts   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;

CREATE INDEX idx_login_challenge_expires ON login_challenge (expires_at);

-- +goose Down
DROP INDEX idx_login_challenge_expires;
DROP TABLE login_challenge;
DROP TABLE user_recovery_code;
DROP TABLE user_totp;
