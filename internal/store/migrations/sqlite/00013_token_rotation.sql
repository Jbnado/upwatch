-- +goose Up

-- Rotação de token com janela de sobreposição.
--
-- rotated_at marca quando o token foi rotacionado — substituído por um
-- sucessor emitido no lugar dele. Nulo é um token que nunca foi
-- rotacionado. Os dois seguem válidos até o dono revogar o antigo, para a
-- rotação não ter janela de corte.
--
-- predecessor_id liga o token novo ao que ele substituiu, e é o que permite
-- a detecção de reuso: uma vez que o sucessor já foi usado — ou seja, o
-- cliente migrou para o segredo novo —, usar o antigo vira sinal de
-- vazamento. ON DELETE SET NULL para revogar o antigo não apagar o novo.
ALTER TABLE api_token ADD COLUMN rotated_at INTEGER;
ALTER TABLE api_token ADD COLUMN predecessor_id INTEGER REFERENCES api_token (id) ON DELETE SET NULL;

CREATE INDEX idx_api_token_predecessor ON api_token (predecessor_id);

-- +goose Down
DROP INDEX idx_api_token_predecessor;
ALTER TABLE api_token DROP COLUMN predecessor_id;
ALTER TABLE api_token DROP COLUMN rotated_at;
