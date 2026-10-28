-- +goose Up

-- O que o próprio serviço disse no último sinal.
--
-- Quem reporta por push muitas vezes sabe exatamente o que deu errado. Sem
-- estas colunas o sinal só dizia "estou vivo", e o script que encontrava
-- uma fila parada tinha de escolher entre mentir ou calar até a janela
-- estourar — e aí o aviso chegava atrasado e sem a causa.
--
-- Os padrões reproduzem o significado que todo sinal já gravado tinha:
-- "up", sem causa e sem medição.
ALTER TABLE push_state ADD COLUMN status     TEXT    NOT NULL DEFAULT 'up';
ALTER TABLE push_state ADD COLUMN message    TEXT    NOT NULL DEFAULT '';
ALTER TABLE push_state ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE push_state DROP COLUMN latency_ms;
ALTER TABLE push_state DROP COLUMN message;
ALTER TABLE push_state DROP COLUMN status;
