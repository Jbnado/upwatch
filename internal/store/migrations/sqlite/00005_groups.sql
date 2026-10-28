-- +goose Up

-- Grupos de monitores: Sistema → Ambiente, dois níveis no máximo.
--
-- O pai não tem cascata de propósito: apagar um sistema com ambientes é
-- recusado pelo store, e a chave estrangeira impede que um descuido no
-- código transforme ambientes em grupos de topo sem ninguém ver.
CREATE TABLE monitor_group (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    slug       TEXT    NOT NULL,
    parent_id  INTEGER          REFERENCES monitor_group (id),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Únicos entre irmãos, não na instalação: "prod" existe em todo sistema.
-- COALESCE porque NULL nunca colide com NULL num índice único, e dois
-- grupos de topo com o mesmo nome passariam.
CREATE UNIQUE INDEX idx_monitor_group_slug ON monitor_group (COALESCE(parent_id, 0), slug);
CREATE UNIQUE INDEX idx_monitor_group_name ON monitor_group (COALESCE(parent_id, 0), name);

-- Grupo do monitor. Nulo é "Sem grupo", e é onde toda instalação anterior
-- cai: nenhum monitor, vínculo ou histórico é tocado.
--
-- Sem REFERENCES aqui, ao contrário do PostgreSQL: o SQLite não sabe
-- remover uma coluna que participa de chave estrangeira, e o Down
-- precisaria reconstruir a tabela mais volumosa de definições. A recusa
-- de apagar grupo em uso é feita pelo store numa instrução só, que vale
-- para os dois bancos.
ALTER TABLE monitor ADD COLUMN group_id INTEGER;
CREATE INDEX idx_monitor_group ON monitor (group_id);

-- O nome do monitor passa a ser único dentro do grupo. Dentro do grupo o
-- nome é curto ("bff"); exigir unicidade global obrigava a repetir o
-- grupo no nome ("rpa-prod bff"), que é o que os grupos vêm resolver.
DROP INDEX idx_monitor_name;
CREATE UNIQUE INDEX idx_monitor_name_in_group ON monitor (COALESCE(group_id, 0), name);

-- Escopo da conta. 1 é "todos os grupos", o que toda conta já tinha:
-- restringir na migração trancaria a instalação para fora.
ALTER TABLE app_user ADD COLUMN all_groups INTEGER NOT NULL DEFAULT 1;

-- Grupos concedidos às contas restritas. Apagar o grupo tira a concessão:
-- a conta fica com menos, nunca com mais.
CREATE TABLE user_group (
    user_id  INTEGER NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL REFERENCES monitor_group (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
) WITHOUT ROWID;

-- Canais que avisam sobre todos os monitores de um grupo, inclusive os
-- criados depois. Convive com monitor_channel, o vínculo por monitor.
CREATE TABLE group_channel (
    group_id   INTEGER NOT NULL REFERENCES monitor_group (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES notification_channel (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, channel_id)
) WITHOUT ROWID;

-- Histórico de mudanças confirmadas de estado. Sem ele a tela de detalhe
-- deduzia mudanças das batidas da janela e dizia "nenhuma mudança" com o
-- monitor fora do ar desde antes de a janela começar.
--
-- Volume baixo — só transições —, por isso fica fora da poda.
CREATE TABLE state_transition (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    from_status TEXT    NOT NULL,
    to_status   TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    cause       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_state_transition_monitor ON state_transition (monitor_id, at);

-- +goose Down
DROP TABLE state_transition;
DROP TABLE group_channel;
DROP TABLE user_group;
ALTER TABLE app_user DROP COLUMN all_groups;
-- Falha se houver nomes repetidos entre grupos: voltar ao nome único
-- global exige renomear antes, e o banco recusar é melhor que escolher
-- sozinho qual monitor perde o nome.
DROP INDEX idx_monitor_name_in_group;
CREATE UNIQUE INDEX idx_monitor_name ON monitor (name);
DROP INDEX idx_monitor_group;
ALTER TABLE monitor DROP COLUMN group_id;
DROP TABLE monitor_group;
