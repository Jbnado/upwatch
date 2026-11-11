-- +goose Up

-- Marco de aviso de vencimento já enviado.
--
-- expiry_warned_days guarda o marco mais fino (em dias de antecedência: 30
-- ou 7) em que o dono já foi avisado de que o token vai vencer, para o aviso
-- sair uma vez por marco e não a cada passagem do verificador. Nulo é um
-- token que nunca gerou aviso. Editar a validade (SetExpiry) o zera: esticar
-- o prazo rearma os marcos.
ALTER TABLE api_token ADD COLUMN expiry_warned_days INTEGER;

-- +goose Down
ALTER TABLE api_token DROP COLUMN expiry_warned_days;
