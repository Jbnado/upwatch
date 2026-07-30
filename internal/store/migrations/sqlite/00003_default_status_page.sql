-- +goose Up

-- Página padrão.
--
-- Uma instalação com uma página só não deveria obrigar ninguém a digitar
-- "/status/estado" — o slug repete o que o caminho já diz. Marcada como
-- padrão, ela responde em "/status", e o slug fica para quem publica
-- várias.
--
-- A coluna é nulável de propósito: guarda 1 na padrão e NULL nas demais.
-- Índice único sobre coluna nulável é a forma de "no máximo uma" que
-- funciona em qualquer banco, porque NULL nunca colide com NULL — não
-- depende de suporte a índice parcial nem de disciplina no código. Duas
-- padrão fariam "/status" responder uma ou outra conforme a ordem da
-- varredura.
ALTER TABLE status_page ADD COLUMN is_default INTEGER;

CREATE UNIQUE INDEX idx_status_page_default ON status_page (is_default);

-- +goose Down
DROP INDEX idx_status_page_default;
ALTER TABLE status_page DROP COLUMN is_default;
