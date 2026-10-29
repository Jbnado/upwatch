-- +goose Up

-- Janelas de manutenção. A janela não desliga nada: as verificações
-- continuam, e o motor troca a falha observada dentro dela por "sem
-- medição", com o motivo na mensagem. Por isso não há coluna nova nas
-- batidas — a marca vai no texto, como a do pai fora do ar.
--
-- starts_at e ends_at são a primeira ocorrência; a recorrente repete o
-- horário de parede no fuso da janela. ended_at é o encerramento antes do
-- previsto, que vale também para as ocorrências seguintes.
CREATE TABLE maintenance_window (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT    NOT NULL,
    message    TEXT    NOT NULL DEFAULT '',
    starts_at  INTEGER NOT NULL,
    ends_at    INTEGER NOT NULL,
    recurrence TEXT    NOT NULL DEFAULT 'none',
    time_zone  TEXT    NOT NULL DEFAULT 'UTC',
    ended_at   INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Alvos. Apagar o monitor ou o grupo tira o alvo da janela: ela passa a
-- cobrir menos, nunca outra coisa que reaproveite o número.
CREATE TABLE maintenance_monitor (
    window_id  INTEGER NOT NULL REFERENCES maintenance_window (id) ON DELETE CASCADE,
    monitor_id INTEGER NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
    PRIMARY KEY (window_id, monitor_id)
) WITHOUT ROWID;

CREATE TABLE maintenance_group (
    window_id INTEGER NOT NULL REFERENCES maintenance_window (id) ON DELETE CASCADE,
    group_id  INTEGER NOT NULL REFERENCES monitor_group (id) ON DELETE CASCADE,
    PRIMARY KEY (window_id, group_id)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE maintenance_group;
DROP TABLE maintenance_monitor;
DROP TABLE maintenance_window;
