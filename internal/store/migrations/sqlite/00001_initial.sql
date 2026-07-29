-- +goose Up

-- Convenções deste schema, válidas para todos os dialetos:
--
--   * Tempo é INTEGER com milissegundos desde a época, sempre UTC. Evita
--     as diferenças de tipo de data entre bancos e mantém o SQL idêntico
--     entre dialetos — a agregação acontece em Go, então nenhuma função de
--     data específica é necessária.
--   * Enums são TEXT. Guardar o número acoplaria os dados à ordem das
--     constantes em Go; o custo em bytes é irrelevante porque o histórico
--     longo vive em rollup, não em heartbeat.
--   * Contadores são INTEGER de 64 bits. Um bucket diário com um check por
--     segundo chega a 86.400 amostras, muito acima do alcance de smallint.

-- Contas de acesso à interface.
CREATE TABLE app_user (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL,
    -- Hash bcrypt. Senha é segredo de baixa entropia, e o custo deliberado
    -- da função é o que inviabiliza testar bilhões de candidatas se o
    -- banco vazar.
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_user_username ON app_user (username);

-- Sessões da interface. O cookie carrega o segredo cru e aqui fica apenas
-- o hash, de modo que ler o banco não conceda acesso a ninguém.
CREATE TABLE session (
    token_hash TEXT    NOT NULL PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Índice para a limpeza periódica das expiradas.
CREATE INDEX idx_session_expires ON session (expires_at);

-- Credenciais de acesso programático.
CREATE TABLE api_token (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    -- SHA-256, não bcrypt: o token tem entropia alta o bastante para
    -- dispensar custo contra força bruta, e esse custo seria pago a cada
    -- requisição — o que viraria um vetor de negação de serviço.
    token_hash   TEXT    NOT NULL,
    -- Fragmento visível do segredo, para o operador reconhecer qual
    -- revogar sem que nada permita reconstruí-lo.
    prefix       TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER,
    expires_at   INTEGER
);

CREATE UNIQUE INDEX idx_api_token_hash ON api_token (token_hash);
CREATE INDEX idx_api_token_user ON api_token (user_id);

CREATE TABLE monitor (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT    NOT NULL,
    type                   TEXT    NOT NULL,
    target                 TEXT    NOT NULL DEFAULT '',
    interval_ms            INTEGER NOT NULL,
    timeout_ms             INTEGER NOT NULL,
    confirmation_threshold INTEGER NOT NULL DEFAULT 3,
    degraded_latency_ms    INTEGER NOT NULL DEFAULT 0,
    -- Configuração do checker, opaca para o banco. Uma coluna por opção de
    -- cada tipo faria a tabela ganhar campos nuláveis a cada checker novo.
    config                 TEXT    NOT NULL DEFAULT '{}',
    -- Reservado para monitores hierárquicos: quando o pai cai, os filhos
    -- não geram alerta próprio. A coluna existe desde a primeira migration
    -- para que a feature não exija migrar histórico depois.
    parent_id              INTEGER          REFERENCES monitor(id) ON DELETE SET NULL,
    enabled                INTEGER NOT NULL DEFAULT 1,
    tags                   TEXT    NOT NULL DEFAULT '[]',
    created_at             INTEGER NOT NULL,
    updated_at             INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_monitor_name ON monitor (name);
CREATE INDEX idx_monitor_parent ON monitor (parent_id);

-- Tabela mais volumosa do banco. Mantida por poucos dias e depois agregada.
CREATE TABLE heartbeat (
    monitor_id INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    -- Reservado para probes distribuídos. Instâncias locais gravam 'local',
    -- de modo que probes remotos entrem sem reprocessar o histórico.
    probe_id   TEXT    NOT NULL DEFAULT 'local',
    ts         INTEGER NOT NULL,
    status     TEXT    NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    message    TEXT    NOT NULL DEFAULT ''
);

-- Consulta por janela de um monitor. Sem este índice composto a consulta
-- degrada para varredura completa — a causa-raiz declarada do gargalo de
-- desempenho do Uptime Kuma.
CREATE INDEX idx_heartbeat_monitor_ts ON heartbeat (monitor_id, ts);
-- Prune global apaga por tempo através de todos os monitores.
CREATE INDEX idx_heartbeat_ts ON heartbeat (ts);

-- Estatística agregada. É o que permite guardar meses de histórico sem
-- guardar meses de batidas cruas.
--
-- A chave primária é natural. Um id auto-incremento somado a um índice
-- único sobre as mesmas colunas desperdiçaria uma coluna e um índice
-- inteiros numa tabela de milhões de linhas.
CREATE TABLE rollup (
    monitor_id      INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    probe_id        TEXT    NOT NULL DEFAULT 'local',
    resolution      TEXT    NOT NULL,
    bucket_start    INTEGER NOT NULL,

    total           INTEGER NOT NULL DEFAULT 0,
    up              INTEGER NOT NULL DEFAULT 0,
    down            INTEGER NOT NULL DEFAULT 0,
    degraded        INTEGER NOT NULL DEFAULT 0,
    -- Checks que rodaram sem observar nada: rede do próprio monitor fora
    -- do ar, ou monitor push ainda sem sinal. Ficam fora do cálculo de
    -- disponibilidade para não cobrar do alvo um problema alheio.
    unknown         INTEGER NOT NULL DEFAULT 0,

    -- Percentis exatos, derivados sempre das batidas cruas. Derivar o
    -- diário a partir do horário produziria percentil de percentil, que
    -- não corresponde a nenhuma medição real.
    latency_samples INTEGER NOT NULL DEFAULT 0,
    latency_avg_ms  REAL    NOT NULL DEFAULT 0,
    latency_min_ms  REAL    NOT NULL DEFAULT 0,
    latency_max_ms  REAL    NOT NULL DEFAULT 0,
    latency_p50_ms  REAL    NOT NULL DEFAULT 0,
    latency_p95_ms  REAL    NOT NULL DEFAULT 0,
    latency_p99_ms  REAL    NOT NULL DEFAULT 0,

    PRIMARY KEY (monitor_id, probe_id, resolution, bucket_start)
) WITHOUT ROWID;

CREATE INDEX idx_rollup_prune ON rollup (resolution, bucket_start);

-- Último sinal recebido de cada monitor push.
--
-- Tabela separada de propósito: o checker de push precisa comparar o
-- instante do sinal com o momento atual, e ele próprio grava uma batida a
-- cada verificação. Se lesse a última batida, leria a que acabou de
-- escrever, e o monitor pareceria eternamente saudável.
CREATE TABLE push_state (
    monitor_id INTEGER NOT NULL PRIMARY KEY REFERENCES monitor (id) ON DELETE CASCADE,
    last_push  INTEGER NOT NULL
) WITHOUT ROWID;

-- Estado confirmado de cada monitor.
--
-- Persistido, e não mantido só em memória, para um reinício não zerar a
-- contagem de confirmação: um alvo prestes a ser declarado fora do ar
-- voltaria à estaca zero e a detecção atrasaria várias janelas.
CREATE TABLE monitor_state (
    monitor_id  INTEGER NOT NULL PRIMARY KEY REFERENCES monitor (id) ON DELETE CASCADE,
    status      TEXT    NOT NULL,
    candidate   TEXT    NOT NULL,
    consecutive INTEGER NOT NULL DEFAULT 0,
    since       INTEGER NOT NULL
) WITHOUT ROWID;

-- Janelas de indisponibilidade confirmada.
CREATE TABLE incident (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    monitor_id  INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    -- Nulo significa em curso.
    resolved_at INTEGER,
    cause       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_incident_monitor ON incident (monitor_id, started_at);

-- No máximo um incidente aberto por monitor. Índice parcial em vez de
-- disciplina no código: dois incidentes abertos ao mesmo tempo tornariam
-- a duração da queda indefinida, e o banco recusa antes de acontecer.
CREATE UNIQUE INDEX idx_incident_open ON incident (monitor_id) WHERE resolved_at IS NULL;

-- Destinos de aviso.
CREATE TABLE notification_channel (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    type       TEXT    NOT NULL,
    -- Configuração do notificador. Guarda a URL do webhook, que é a
    -- credencial de quem pode publicar no canal.
    config     TEXT    NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_channel_name ON notification_channel (name);

-- Quais canais avisam sobre quais monitores.
CREATE TABLE monitor_channel (
    monitor_id INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES notification_channel (id) ON DELETE CASCADE,
    PRIMARY KEY (monitor_id, channel_id)
) WITHOUT ROWID;

-- Marca d'água da agregação: até onde cada resolução já foi processada.
-- Permite que um reinício não reprocesse nem pule buckets.
CREATE TABLE rollup_state (
    resolution  TEXT    NOT NULL PRIMARY KEY,
    last_bucket INTEGER NOT NULL
) WITHOUT ROWID;

-- +goose Down
DROP TABLE rollup_state;
DROP TABLE monitor_channel;
DROP TABLE notification_channel;
DROP TABLE incident;
DROP TABLE monitor_state;
DROP TABLE push_state;
DROP TABLE rollup;
DROP TABLE heartbeat;
DROP TABLE monitor;
DROP TABLE api_token;
DROP TABLE session;
DROP TABLE app_user;
