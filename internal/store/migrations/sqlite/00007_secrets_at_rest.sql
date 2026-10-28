-- +goose Up

-- Registro-sentinela da chave de segredos.
--
-- As credenciais de monitores e canais passam a ficar cifradas na coluna
-- config, e quem as cifra e decifra é o store, em Go: a chave não passa
-- pelo SQL. Este registro guarda um valor conhecido cifrado com a chave
-- da instalação. Na partida, o store o abre: se não abrir, a chave
-- configurada não é a do banco, e o UpWatch recusa subir em vez de
-- rodar sem conseguir verificar nada. Sem o sentinela, uma chave errada
-- num banco ainda sem credencial passaria, e a primeira senha cadastrada
-- ficaria cifrada com uma chave diferente da das seguintes.
--
-- Uma linha só, garantida pelo CHECK.
--
-- A cifragem dos valores em texto puro que já existem, e a troca do token
-- do push pelo SHA-256 dele na coluna push_token, também acontecem no
-- store, a cada partida e de forma idempotente: o SQLite não tem SHA-256,
-- e cifrar exige a chave. Por isso o Down desfaz só a tabela. Voltar o
-- binário a uma versão anterior depois da cifragem não é suportado: ela
-- leria a cifra como se fosse a senha.
CREATE TABLE secret_key_check (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value TEXT    NOT NULL
);

-- +goose Down
DROP TABLE secret_key_check;
