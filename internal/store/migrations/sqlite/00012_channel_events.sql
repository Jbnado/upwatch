-- +goose Up

-- Quais eventos cada canal recebe.
--
-- Até aqui um canal recebia toda transição confirmada — queda, volta,
-- degradação. Esta coluna guarda o conjunto que ele assina, como um array
-- JSON de tipos nomeados por família ("monitor.down", "monitor.up",
-- "monitor.degraded"). Vazio recebe tudo, e é o valor que toda linha já
-- existente herda: a atualização não muda quem recebe o quê.
--
-- Nomear por família é de propósito: a #128 acrescenta a família de conta
-- (token vencendo, token revogado) sem tocar nesta coluna nem no filtro —
-- um tipo novo é só mais uma entrada possível neste array.
ALTER TABLE notification_channel ADD COLUMN events TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE notification_channel DROP COLUMN events;
