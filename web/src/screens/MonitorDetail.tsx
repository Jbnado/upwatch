import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { Heartbeat, Monitor, Rollup, Status } from "../api/types";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import { Alert, Button, Empty, StatusDot } from "../components/ui";
import { ago, duration, latency, stamp, uptime } from "../lib/format";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { navigate } from "../lib/router";

/**
 * Detalhe de um alvo.
 *
 * A faixa ocupa a largura inteira e ganha altura, porque aqui ela é o
 * objeto de estudo e não um resumo de linha. As medidas ficam acima dela,
 * na mesma coluna do painel, para o olho não precisar reaprender onde
 * cada número mora.
 */
export function MonitorDetail({ id }: { id: number }) {
  const [range, setRange] = useState<Range>(DEFAULT_RANGE);
  const [monitor, setMonitor] = useState<Monitor | null>(null);
  const [samples, setSamples] = useState<PulseSample[]>([]);
  const [beats, setBeats] = useState<Heartbeat[]>([]);
  const [rollups, setRollups] = useState<Rollup[]>([]);
  const [cursor, setCursor] = useState<PulseSample | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const { from, to } = windowFor(range);
      const m = await api.getMonitor(id);
      setMonitor(m);

      if (range.source === "raw") {
        const { items } = await api.heartbeats(id, { from, to, limit: 300 });
        setBeats(items);
        setRollups([]);
        setSamples(
          items.map((hb) => ({ at: hb.timestamp, status: hb.status, latencyMs: hb.latency_ms })),
        );
      } else {
        const { items } = await api.rollups(id, {
          from,
          to,
          resolution: range.source === "hourly" ? "hourly" : "daily",
        });
        setRollups(items);
        setBeats([]);
        setSamples(
          items.map((r) => ({
            at: r.bucket_start,
            status: r.down > 0 ? "down" : r.degraded > 0 ? "degraded" : r.up > 0 ? "up" : "unknown",
            latencyMs: r.latency_p95_ms,
          })),
        );
      }
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar este monitor.");
    }
  }, [id, range]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  useEffect(() => {
    const t = setInterval(() => void carregar(), 30_000);
    return () => clearInterval(t);
  }, [carregar]);

  if (erro) {
    return (
      <div className="mx-auto max-w-6xl px-5 py-6">
        <Alert>{erro}</Alert>
      </div>
    );
  }
  if (!monitor) {
    return <p className="eyebrow mx-auto max-w-6xl px-5 py-6">carregando</p>;
  }

  const atual = statusFrom(samples);
  const stats = summarise(beats, rollups);

  async function remover() {
    if (!confirm(`Remover "${monitor!.name}" e todo o seu histórico?`)) return;

    await api.deleteMonitor(monitor!.id);
    navigate({ name: "board" });
  }

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-6">
      <nav>
        <button
          onClick={() => navigate({ name: "board" })}
          className="eyebrow hover:text-ink"
        >
          ← todos os monitores
        </button>
      </nav>

      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <StatusDot status={atual} withLabel />
            <h1 className="text-[22px] font-semibold tracking-tight">{monitor.name}</h1>
          </div>
          <p className="tabular text-[13px] text-ink-3">
            {monitor.type} · {monitor.target || "recebe sinal do próprio serviço"} · a cada{" "}
            {duration(monitor.interval_seconds)}
          </p>
        </div>

        <div className="flex items-center gap-2">
          <Button onClick={() => navigate({ name: "monitor-edit", id: monitor.id })}>Editar</Button>
          <Button variant="danger" onClick={remover}>
            Remover
          </Button>
        </div>
      </header>

      <section className="flex flex-col gap-3">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div className="flex gap-8">
            <Stat label="disponibilidade" value={uptime(stats.uptimePercent)} />
            <Stat label="p50" value={latency(stats.p50)} />
            <Stat label="p95" value={latency(stats.p95)} />
            <Stat label="p99" value={latency(stats.p99)} />
          </div>
          <RangePicker value={range} onChange={setRange} />
        </div>

        {/* Aqui a faixa é o objeto de estudo, então ganha altura e barras
            mais largas. O eixo sob ela mostra até onde o período vai,
            mesmo onde ainda não há histórico. */}
        <Pulse
          samples={samples}
          onInspect={setCursor}
          rangeLabel={range.label}
          height={96}
          maxBarWidth={16}
          baseline
        />

        {/* Leitura do cursor em posição fixa, como o mostrador de um
            osciloscópio: um balão flutuante obrigaria o olho a persegui-lo
            enquanto a mão percorre a faixa. */}
        <div className="flex h-5 items-center gap-4">
          {cursor ? (
            <>
              <span className="tabular text-[12px] text-ink-2">{stamp(cursor.at)}</span>
              <StatusDot status={cursor.status} withLabel />
              <span className="tabular text-[12px] text-ink-2">{latency(cursor.latencyMs)}</span>
            </>
          ) : (
            <span className="eyebrow">passe o cursor sobre a faixa para inspecionar</span>
          )}
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">últimas mudanças de estado</h2>
        <Events beats={beats} />
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">exportar</h2>
        <div className="flex gap-2">
          {(["csv", "json"] as const).map((formato) => (
            <a
              key={formato}
              href={api.exportUrl(monitor.id, { ...windowFor(range), format: formato })}
              className="inline-flex h-8 items-center rounded-[3px] border border-line-strong px-3 text-[13px] hover:bg-sunken"
            >
              Baixar {formato.toUpperCase()}
            </a>
          ))}
        </div>
      </section>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col">
      <span className="eyebrow">{label}</span>
      <span className="tabular text-[22px] leading-tight tracking-tight">{value}</span>
    </div>
  );
}

/**
 * Events lista apenas as transições.
 *
 * Uma tabela com todas as verificações seria milhares de linhas dizendo
 * "continua no ar". O que se procura depois de um incidente é quando
 * mudou.
 */
function Events({ beats }: { beats: Heartbeat[] }) {
  const transicoes = beats.filter((hb, i) => i > 0 && hb.status !== beats[i - 1]!.status).reverse();

  if (transicoes.length === 0) {
    return (
      <Empty
        title="Nenhuma mudança no período"
        description="O estado se manteve do início ao fim da janela escolhida."
      />
    );
  }

  return (
    <ul className="border-t border-line">
      {transicoes.slice(0, 20).map((hb, i) => (
        <li
          key={hb.timestamp + i}
          className="flex items-baseline justify-between gap-4 border-b border-line py-2"
        >
          <span className="flex items-baseline gap-3">
            <StatusDot status={hb.status} withLabel />
            {hb.message && <span className="truncate text-[13px] text-ink-2">{hb.message}</span>}
          </span>
          <span className="flex shrink-0 items-baseline gap-3">
            <span className="tabular text-[12px] text-ink-3">{stamp(hb.timestamp)}</span>
            <span className="tabular text-[12px] text-ink-3">{ago(hb.timestamp)}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

function statusFrom(samples: PulseSample[]): Status {
  return samples.at(-1)?.status ?? "unknown";
}

function summarise(beats: Heartbeat[], rollups: Rollup[]) {
  if (rollups.length > 0) {
    const observadas = rollups.reduce((n, r) => n + r.up + r.degraded + r.down, 0);
    const fora = rollups.reduce((n, r) => n + r.down, 0);

    return {
      uptimePercent: observadas ? ((observadas - fora) / observadas) * 100 : 0,
      p50: max(rollups.map((r) => r.latency_p50_ms)),
      p95: max(rollups.map((r) => r.latency_p95_ms)),
      p99: max(rollups.map((r) => r.latency_p99_ms)),
    };
  }

  const observadas = beats.filter((hb) => hb.status !== "unknown");
  const respondidas = beats.filter((hb) => hb.status === "up" || hb.status === "degraded");
  const latencias = respondidas.map((hb) => hb.latency_ms);

  return {
    uptimePercent: observadas.length
      ? (observadas.filter((hb) => hb.status !== "down").length / observadas.length) * 100
      : 0,
    p50: percentile(latencias, 50),
    p95: percentile(latencias, 95),
    p99: percentile(latencias, 99),
  };
}

/**
 * max é usado sobre percentis de agregados.
 *
 * Somar ou tirar média de percentis produziria um número que não
 * corresponde a medição alguma; o pior percentil da janela é uma
 * afirmação verdadeira sobre o período.
 */
function max(values: number[]): number {
  return values.length ? Math.max(...values) : 0;
}

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;

  const ordenados = [...values].sort((a, b) => a - b);
  const posto = Math.ceil((p / 100) * ordenados.length);
  return ordenados[Math.min(Math.max(posto, 1), ordenados.length) - 1]!;
}
