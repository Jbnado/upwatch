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

-- Identidade do sinal e marca de "já contado".
--
-- O sinal fica guardado até chegar o seguinte, e cada verificação o relê.
-- Sem saber se ele já foi contado, o mesmo "down" viraria falha nova a
-- cada releitura, e um sinal só abriria incidente sozinho depois de N
-- verificações. A identidade é um contador, não o instante: dois sinais
-- no mesmo milissegundo são dois sinais.
--
-- A linha que já existe chega com os dois em zero, ou seja, contada: o
-- sinal dela já passou pelo motor antes da atualização.
ALTER TABLE push_state ADD COLUMN signal_id    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE push_state ADD COLUMN evaluated_id INTEGER NOT NULL DEFAULT 0;

-- Instante a partir do qual a espera do push recomeça.
--
-- A janela era contada do último sinal. Trocar a agenda ou religar um
-- monitor parado o derrubava na hora, cobrando uma execução que ninguém
-- estava vigiando. A base passa a ser o mais recente entre o último sinal
-- e esta mudança. updated_at não serve: muda em qualquer edição, e
-- renomear não pode adiar um alarme legítimo.
--
-- O monitor que já existe recomeça da criação, que é de onde a primeira
-- janela já era contada.
ALTER TABLE monitor ADD COLUMN rearmed_at INTEGER NOT NULL DEFAULT 0;
UPDATE monitor SET rearmed_at = created_at;

-- O token do push em coluna própria, indexada e única.
--
-- O /push não tem login, e achar o dono do token percorria todos os
-- monitores a cada requisição: qualquer um podia provocar uma varredura
-- da tabela à vontade. E nada impedia dois monitores com o mesmo token —
-- o sinal de um caía no outro, e o que ficava sem sinal caía por silêncio
-- com o serviço no ar.
--
-- O token continua na configuração, que é o que a interface lê; a coluna
-- é a cópia que a busca e a unicidade usam. Nulo fora do push: monitores
-- sem token não disputam nada.
--
-- Se dois monitores já compartilham um token, só o de menor id fica com
-- ele, que é o que a busca antiga achava primeiro — o sinal continua
-- chegando onde chegava. O outro fica sem coluna, e salvá-lo de novo
-- exige um token próprio.
ALTER TABLE monitor ADD COLUMN push_token TEXT;
UPDATE monitor SET push_token = json_extract(config, '$.token')
WHERE type = 'push'
  AND json_valid(config)
  AND json_type(config, '$.token') = 'text'
  AND json_extract(config, '$.token') <> ''
  AND id = (
    SELECT MIN(o.id) FROM monitor o
    WHERE o.type = 'push' AND json_valid(o.config)
      AND json_extract(o.config, '$.token') = json_extract(monitor.config, '$.token')
  );
CREATE UNIQUE INDEX idx_monitor_push_token ON monitor (push_token);

-- +goose Down
DROP INDEX idx_monitor_push_token;
ALTER TABLE monitor DROP COLUMN push_token;
ALTER TABLE monitor DROP COLUMN rearmed_at;
ALTER TABLE push_state DROP COLUMN evaluated_id;
ALTER TABLE push_state DROP COLUMN signal_id;
ALTER TABLE push_state DROP COLUMN latency_ms;
ALTER TABLE push_state DROP COLUMN message;
ALTER TABLE push_state DROP COLUMN status;
