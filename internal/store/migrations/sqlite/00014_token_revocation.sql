-- +goose Up

-- Revogação que retém o registro.
--
-- Revogar um token deixa de apagá-lo: a linha fica, marcada com quando foi
-- revogada (revoked_at), por que (revoked_reason: manual, reuso,
-- rebaixamento, ...) e por quem (revoked_by — nulo para revogação do
-- sistema, como a detecção de reuso, ou da linha de comando). Um token
-- revogado não autentica mais nem aparece na listagem ativa, mas o registro
-- sobrevive para a auditoria responder "quem revogou o quê, quando e por
-- quê". ON DELETE SET NULL em revoked_by: apagar a conta de quem revogou não
-- apaga o registro da revogação.
ALTER TABLE api_token ADD COLUMN revoked_at INTEGER;
ALTER TABLE api_token ADD COLUMN revoked_reason TEXT;
ALTER TABLE api_token ADD COLUMN revoked_by INTEGER REFERENCES app_user (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE api_token DROP COLUMN revoked_by;
ALTER TABLE api_token DROP COLUMN revoked_reason;
ALTER TABLE api_token DROP COLUMN revoked_at;
