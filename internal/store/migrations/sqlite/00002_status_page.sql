-- +goose Up

-- Páginas públicas de estado.
--
-- Duas camadas, separadas de propósito. As barras de histórico saem de
-- heartbeat e rollup, que a máquina preenche. O relato — announcement e
-- suas atualizações — é escrito por uma pessoa. A causa detectada pela
-- sonda cita host e porta internos, e por isso nunca alimenta a página:
-- o que sai daqui é só o que alguém decidiu contar.

CREATE TABLE status_page (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Vai na URL. A validação no domínio recusa acento, espaço, barra e
    -- maiúscula: é endereço colado em chat e digitado de memória.
    slug         TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    -- Latência desligada por padrão: é informação competitiva para parte
    -- de quem publica, e "está no ar?" não depende dela.
    show_latency INTEGER NOT NULL DEFAULT 0,
    -- Fuso do recorte diário. Vazio significa UTC. Sem ele o dia começa
    -- em UTC e a queda das 22h aparece no dia seguinte para quem está no
    -- Brasil.
    time_zone    TEXT    NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- Slug duplicado faria duas páginas responderem no mesmo endereço, e qual
-- delas responde dependeria da ordem da varredura.
CREATE UNIQUE INDEX idx_status_page_slug ON status_page (slug);

-- Agrupamento de componentes: "API", "Console", "Webhooks".
CREATE TABLE status_page_group (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    page_id  INTEGER NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
    name     TEXT    NOT NULL,
    -- Ordem editorial: o que o cliente mais usa vem primeiro, e isso não
    -- se deduz de dado nenhum.
    position INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_status_page_group_page ON status_page_group (page_id, position);

-- Quais monitores a página publica.
CREATE TABLE status_page_component (
    page_id    INTEGER NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
    monitor_id INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    -- SET NULL e não CASCADE: desfazer um agrupamento reorganiza a
    -- página, não despublica o componente.
    group_id   INTEGER          REFERENCES status_page_group (id) ON DELETE SET NULL,
    -- Nome público. O monitor pode se chamar "api-prod-us-east-1" na
    -- operação e aparecer como "API" para quem lê; sem isso, publicar a
    -- página obrigaria a renomear o monitor ou a entregar a convenção de
    -- nomes da infraestrutura.
    label      TEXT    NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (page_id, monitor_id)
) WITHOUT ROWID;

CREATE INDEX idx_status_page_component_group ON status_page_component (group_id);

-- Relato público de um incidente.
CREATE TABLE announcement (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT    NOT NULL,
    impact      TEXT    NOT NULL,
    phase       TEXT    NOT NULL,
    -- Alcance explícito, e não deduzido de uma lista vazia de
    -- componentes: apagar um monitor esvazia a lista por cascata, e um
    -- relato sobre serviço desativado passaria a aparecer na página de
    -- todo mundo sem ninguém ver acontecer.
    global      INTEGER NOT NULL DEFAULT 0,
    -- SET NULL: apagar um monitor leva seus incidentes por cascata, mas o
    -- que foi comunicado publicamente é registro e não pode desaparecer
    -- porque alguém apagou um alvo meses depois.
    incident_id INTEGER          REFERENCES incident (id) ON DELETE SET NULL,
    started_at  INTEGER NOT NULL,
    resolved_at INTEGER,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- A página lê por janela, do mais recente para o mais antigo.
CREATE INDEX idx_announcement_started ON announcement (started_at);

-- Componentes afetados pelo relato.
CREATE TABLE announcement_component (
    announcement_id INTEGER NOT NULL REFERENCES announcement (id) ON DELETE CASCADE,
    monitor_id      INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    PRIMARY KEY (announcement_id, monitor_id)
) WITHOUT ROWID;

-- A linha do tempo: investigando, identificado, monitorando, resolvido.
--
-- É o que responde à pergunta que quem espera realmente faz — não "o que
-- quebrou", mas "vocês já sabem, e falta muito?".
CREATE TABLE announcement_update (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    announcement_id INTEGER NOT NULL REFERENCES announcement (id) ON DELETE CASCADE,
    phase           TEXT    NOT NULL,
    body            TEXT    NOT NULL,
    published_at    INTEGER NOT NULL
);

CREATE INDEX idx_announcement_update_parent
    ON announcement_update (announcement_id, published_at);

-- +goose Down
DROP TABLE announcement_update;
DROP TABLE announcement_component;
DROP TABLE announcement;
DROP TABLE status_page_component;
DROP TABLE status_page_group;
DROP TABLE status_page;
