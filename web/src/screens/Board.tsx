import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import type { Monitor, Status } from "../api/types";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import { Button, Empty, StatusDot } from "../components/ui";
import { ago, latency, uptime } from "../lib/format";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { navigate, pool } from "../lib/router";

/**
 * O painel.
 *
 * Uma lista densa em vez de cartões: cartões com sombra e respiro são o
 * visual padrão de painel, e desperdiçam a tela justamente onde ela mais
 * serve — comparar dezenas de alvos de relance. Aqui cada linha é um
 * canal do instrumento.
 */

type Reading = {
  monitor: Monitor;
  samples: PulseSample[];
  status: Status;
  uptimePercent: number;
  p95: number;
};

export function Board() {
  const [range, setRange] = useState<Range>(DEFAULT_RANGE);
  const [readings, setReadings] = useState<Reading[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const { from, to } = windowFor(range);
      const page = await api.listMonitors({ limit: 200 });

      // Teto de simultaneidade: uma instalação grande abriria requisições
      // demais de uma vez e o navegador as enfileiraria sem ordem.
      const lidos = await pool(page.items, 6, (m) => read(m, range, from, to));

      setReadings(lidos);
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar os monitores.");
    }
  }, [range]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  // Reconsulta periódica: durante um incidente ninguém quer apertar
  // atualizar para saber se o serviço voltou.
  useEffect(() => {
    const id = setInterval(() => void carregar(), 30_000);
    return () => clearInterval(id);
  }, [carregar]);

  const resumo = useMemo(() => summarise(readings ?? []), [readings]);

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-5 px-5 py-6">
      <header className="flex flex-wrap items-baseline justify-between gap-4">
        <Summary {...resumo} total={readings?.length ?? 0} />
        <div className="flex items-center gap-3">
          <RangePicker value={range} onChange={setRange} />
          <Button variant="primary" onClick={() => navigate({ name: "monitor-new" })}>
            Novo monitor
          </Button>
        </div>
      </header>

      {erro && <p className="border-l-2 border-down bg-down-dim px-3 py-2 text-[13px]">{erro}</p>}

      {readings === null ? (
        <p className="eyebrow">carregando</p>
      ) : readings.length === 0 ? (
        <Empty
          title="Nenhum alvo sendo vigiado"
          description="Cadastre o primeiro serviço para começar a acompanhar disponibilidade e latência."
          action={
            <Button variant="primary" onClick={() => navigate({ name: "monitor-new" })}>
              Cadastrar monitor
            </Button>
          }
        />
      ) : (
        <div>
          {/* Rótulos de coluna uma vez só. Repeti-los em cada linha
              encheria a lista de texto que não muda e competiria com os
              números, que são o que se lê. */}
          <div className="flex items-center gap-5 border-y border-line px-1 py-1.5">
            <span className="eyebrow flex-1">alvo</span>
            <span className="eyebrow w-[196px] shrink-0">
              {range.label} de histórico
            </span>
            <span className="flex w-[164px] shrink-0 justify-between gap-4">
              <span className="eyebrow">disponibilidade</span>
              <span className="eyebrow">p95</span>
            </span>
          </div>

          {readings.map((reading) => (
            <Row key={reading.monitor.id} reading={reading} rangeLabel={range.label} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * Row é um canal do painel.
 *
 * Nome e alvo à esquerda, medidas alinhadas à direita em coluna tabular,
 * e a faixa ocupando a largura restante. Os números ficam na mesma
 * posição em toda linha, então o olho desce a coluna comparando em vez de
 * caçar.
 */
function Row({ reading, rangeLabel }: { reading: Reading; rangeLabel: string }) {
  const { monitor, samples, status, uptimePercent, p95 } = reading;

  // Duas janelas sem verificação já é anomalia: o agendador deveria ter
  // passado por aqui, e não passou.
  const ultima = samples.at(-1);
  const stale =
    ultima !== undefined &&
    Date.now() - new Date(ultima.at).getTime() > monitor.interval_seconds * 2000;

  return (
    <button
      type="button"
      onClick={() => navigate({ name: "monitor", id: monitor.id })}
      // Uma linha por alvo. A faixa fica entre o nome e os números, no
      // mesmo eixo, para o olho descer a lista sem trocar de trilha.
      className="flex w-full items-center gap-5 border-b border-line px-1 py-2 text-left transition-colors hover:bg-sunken"
    >
      <span className="flex min-w-0 flex-1 items-baseline gap-2">
        <StatusDot status={status} />
        <span className="truncate text-[13.5px] font-medium text-ink">{monitor.name}</span>
        <span className="tabular truncate text-[12px] text-ink-3">{monitor.target}</span>
        {stale && (
          // Monitor que parou de ser verificado é uma falha própria, não
          // do alvo; sem este aviso ela passaria por "tudo estável".
          <span className="shrink-0 text-[11px] text-degraded">
            sem verificar {ago(samples.at(-1)!.at)}
          </span>
        )}
      </span>

      <span className="w-[196px] shrink-0">
        <Pulse samples={samples} rangeLabel={rangeLabel} height={26} />
      </span>

      {/* Largura fixa e sem quebra: os números precisam ficar na mesma
          coluna em toda linha para serem comparados de relance, e uma
          unidade que cai para a linha de baixo destrói esse alinhamento. */}
      <span className="tabular flex w-[164px] shrink-0 justify-between gap-4 whitespace-nowrap text-[13px]">
        <span>{uptime(uptimePercent)}</span>
        <span>{p95 > 0 ? latency(p95) : "—"}</span>
      </span>
    </button>
  );
}

/**
 * Summary é a primeira coisa lida.
 *
 * "Tudo no ar" fica quieto de propósito; o que exige ação aparece
 * primeiro e é o único elemento colorido do cabeçalho.
 */
function Summary({
  up,
  degraded,
  down,
  total,
}: {
  up: number;
  degraded: number;
  down: number;
  total: number;
}) {
  const tudoBem = down === 0 && degraded === 0 && total > 0;

  return (
    <div className="flex items-baseline gap-3">
      <span className="text-[15px] font-semibold tracking-tight text-ink">UpWatch</span>

      {total === 0 ? null : tudoBem ? (
        <span className="text-[13px] text-ink-2">
          <span className="tabular">{up}</span> alvos, todos no ar
        </span>
      ) : (
        <span className="flex items-baseline gap-3 text-[13px]">
          {down > 0 && (
            <span className="font-medium text-down">
              <span className="tabular">{down}</span> fora do ar
            </span>
          )}
          {degraded > 0 && (
            <span className="font-medium text-degraded">
              <span className="tabular">{degraded}</span> degradado{degraded > 1 ? "s" : ""}
            </span>
          )}
          <span className="text-ink-3">
            <span className="tabular">{up}</span> no ar
          </span>
        </span>
      )}
    </div>
  );
}

function summarise(readings: Reading[]) {
  return readings.reduce(
    (acc, r) => ({ ...acc, [r.status]: acc[r.status] + 1 }),
    { up: 0, degraded: 0, down: 0, unknown: 0 } as Record<Status, number>,
  );
}

/**
 * read busca a série do monitor na camada adequada ao período.
 *
 * Janela curta lê batidas cruas; janela longa lê agregados, que é o que
 * permite olhar um ano sem varrer milhões de linhas.
 */
async function read(monitor: Monitor, range: Range, from: string, to: string): Promise<Reading> {
  if (range.source === "raw") {
    const { items } = await api.heartbeats(monitor.id, { from, to, limit: 200 });

    const samples = items.map((hb) => ({
      at: hb.timestamp,
      status: hb.status,
      latencyMs: hb.latency_ms,
    }));
    const respondidas = items.filter((hb) => hb.status === "up" || hb.status === "degraded");
    const observadas = items.filter((hb) => hb.status !== "unknown");

    return {
      monitor,
      samples,
      status: items.at(-1)?.status ?? "unknown",
      uptimePercent: observadas.length
        ? (observadas.filter((hb) => hb.status !== "down").length / observadas.length) * 100
        : 0,
      p95: percentile(respondidas.map((hb) => hb.latency_ms), 95),
    };
  }

  const { items } = await api.rollups(monitor.id, {
    from,
    to,
    resolution: range.source === "hourly" ? "hourly" : "daily",
  });

  const samples = items.map((r) => ({
    at: r.bucket_start,
    status: bucketStatus(r.up, r.degraded, r.down),
    latencyMs: r.latency_p95_ms,
  }));

  const observadas = items.reduce((n, r) => n + r.up + r.degraded + r.down, 0);
  const foraDoAr = items.reduce((n, r) => n + r.down, 0);

  return {
    monitor,
    samples,
    status: samples.at(-1)?.status ?? "unknown",
    uptimePercent: observadas ? ((observadas - foraDoAr) / observadas) * 100 : 0,
    p95: percentile(items.map((r) => r.latency_p95_ms).filter((v) => v > 0), 95),
  };
}

/**
 * bucketStatus resume um período agregado num estado.
 *
 * Qualquer falha no intervalo pesa mais que o resto: um bucket com uma
 * hora de indisponibilidade não é "no ar" só porque a maioria das
 * verificações passou.
 */
function bucketStatus(up: number, degraded: number, down: number): Status {
  if (down > 0) return "down";
  if (degraded > 0) return "degraded";
  if (up > 0) return "up";
  return "unknown";
}

/** percentile por posto mais próximo, igual ao servidor. */
function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;

  const ordenados = [...values].sort((a, b) => a - b);
  const posto = Math.ceil((p / 100) * ordenados.length);
  return ordenados[Math.min(Math.max(posto, 1), ordenados.length) - 1]!;
}
