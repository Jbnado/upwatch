-- +goose Up

-- Lembrete enquanto a queda segue aberta, e o reconhecimento que o cala.
--
-- O intervalo mora no monitor e no grupo, nulo em ambos: nulo é "herda"
-- (do ambiente, do sistema, da instalação), zero é "desligado". Gravar o
-- padrão em cada linha faria mudar o padrão não alcançar quem nunca o
-- escolheu.
ALTER TABLE monitor ADD COLUMN reminder_interval_ms INTEGER;
ALTER TABLE monitor_group ADD COLUMN reminder_interval_ms INTEGER;

-- reminded_at é o último lembrete enviado. Gravado, e não só em memória,
-- para um reinício no meio da madrugada não reenviar tudo de uma vez nem
-- recomeçar a contagem do zero.
ALTER TABLE incident ADD COLUMN reminded_at INTEGER;

-- Quem reconheceu fica pelo nome, sem chave estrangeira: apagar a conta
-- depois não pode reescrever o histórico de quem estava olhando a queda.
ALTER TABLE incident ADD COLUMN acknowledged_at INTEGER;
ALTER TABLE incident ADD COLUMN acknowledged_by TEXT;
ALTER TABLE incident ADD COLUMN ack_note TEXT;

-- +goose Down
ALTER TABLE incident DROP COLUMN ack_note;
ALTER TABLE incident DROP COLUMN acknowledged_by;
ALTER TABLE incident DROP COLUMN acknowledged_at;
ALTER TABLE incident DROP COLUMN reminded_at;
ALTER TABLE monitor_group DROP COLUMN reminder_interval_ms;
ALTER TABLE monitor DROP COLUMN reminder_interval_ms;
