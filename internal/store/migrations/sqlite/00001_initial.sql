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

-- Marca d'água da agregação: até onde cada resolução já foi processada.
-- Permite que um reinício não reprocesse nem pule buckets.
CREATE TABLE rollup_state (
    resolution  TEXT    NOT NULL PRIMARY KEY,
    last_bucket INTEGER NOT NULL
) WITHOUT ROWID;

-- +goose Down
DROP TABLE rollup_state;
DROP TABLE rollup;
DROP TABLE heartbeat;
DROP TABLE monitor;
