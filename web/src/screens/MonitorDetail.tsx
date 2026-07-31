import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { Heartbeat, Incident, Monitor, Summary as SummaryDTO } from "../api/types";
import { MonitorChannels } from "./Channels";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import { Alert, Button, LinkButton, Loading, Nothing, StatusDot, TextLink } from "../components/ui";
import { ago, duration, latency, stamp, uptime } from "../lib/format";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { navigate } from "../lib/router";

/** Mesma resolução de faixa do painel: as duas telas desenham igual. */
const PONTOS_DA_FAIXA = 60;

/**
 * EVENTOS é quantas batidas cruas a lista de mudanças de estado lê.
 *
 * Aqui o dado cru é o certo: a lista mostra transições reais, com horário
 * e causa, e não um resumo. O servidor devolve as mais recentes da janela.
 */
const EVENTOS = 300;

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
  const [resumo, setResumo] = useState<SummaryDTO | null>(null);
  const [beats, setBeats] = useState<Heartbeat[]>([]);
  const [cursor, setCursor] = useState<PulseSample | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const { from, to } = windowFor(range);

      // Mesma fonte do painel, e nenhum cálculo aqui. Enquanto as duas
      // telas somavam por conta própria, a mesma janela podia render dois
      // números — e nada acusava, porque cada cópia estava internamente
      // coerente.
      const [m, resumos, historico] = await Promise.all([
        api.getMonitor(id),
        api.summaries({ from, to, buckets: PONTOS_DA_FAIXA }),
        api.heartbeats(id, { from, to, limit: EVENTOS }),
      ]);

      setMonitor(m);
      setResumo(resumos.items.find((s) => s.monitor_id === id) ?? null);
      setBeats(historico.items);
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
    return (
      <div className="mx-auto max-w-6xl px-5 py-6">
        <Loading what="este monitor" />
      </div>
    );
  }

  const samples: PulseSample[] =
    resumo?.series.map((p: SummaryDTO["series"][number]) => ({
      at: p.at,
      status: p.status,
      latencyMs: p.latency_ms,
    })) ?? [];

  const atual = resumo?.status ?? "unknown";
  const stats = {
    uptimePercent: resumo?.uptime_percent ?? null,
    p50: resumo?.latency_p50_ms ?? null,
    p95: resumo?.latency_p95_ms ?? null,
    p99: resumo?.latency_p99_ms ?? null,
  };

  async function remover() {
    if (!confirm(`Remover "${monitor!.name}" e todo o seu histórico?`)) return;

    await api.deleteMonitor(monitor!.id);
    navigate({ name: "board" });
  }

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-6">
      <nav>
        <TextLink onClick={() => navigate({ name: "board" })}>← todos os monitores</TextLink>
      </nav>

      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex flex-col gap-1.5">
          <div className="flex items-center gap-2">
            <StatusDot status={atual} withLabel />
            <h1 className="text-title font-semibold tracking-tight">{monitor.name}</h1>
          </div>
          <p className="tabular text-body text-ink-3">
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
          {/* Ausência já vem como null do resumo, e as funções de formato
              a traduzem em travessão — nenhuma tela precisa mais decidir
              sozinha o que fazer com dado que não existe. */}
          <div className="flex gap-6">
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
              <span className="tabular text-small text-ink-2">{stamp(cursor.at)}</span>
              <StatusDot status={cursor.status} withLabel />
              <span className="tabular text-small text-ink-2">{latency(cursor.latencyMs)}</span>
            </>
          ) : (
            <span className="eyebrow">passe o cursor sobre a faixa para inspecionar</span>
          )}
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">quem é avisado quando este alvo cair</h2>
        <MonitorChannels monitorID={monitor.id} />
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">quedas confirmadas</h2>
        <Incidents monitorID={monitor.id} />
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">últimas mudanças de estado</h2>
        <Events beats={beats} />
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="eyebrow">exportar</h2>
        {/* LinkButton e não Button: baixar é navegação, e como âncora
            ganha copiar endereço e abrir em nova aba de graça. */}
        <div className="flex gap-2">
          {(["csv", "json"] as const).map((formato) => (
            <LinkButton
              key={formato}
              href={api.exportUrl(monitor.id, { ...windowFor(range), format: formato })}
            >
              Baixar {formato.toUpperCase()}
            </LinkButton>
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
      <span className="tabular text-title leading-tight tracking-tight">{value}</span>
    </div>
  );
}

/**
 * Incidents lista as quedas confirmadas.
 *
 * Diferente da lista de transições: aqui só entra o que passou pelo limiar
 * de confirmação e gerou alerta. É o histórico que se leva para uma
 * conversa sobre disponibilidade.
 */
function Incidents({ monitorID }: { monitorID: number }) {
  const [items, setItems] = useState<Incident[] | null>(null);

  useEffect(() => {
    void api.incidents({ monitor_id: monitorID, limit: 20 }).then((page) => setItems(page.items));
  }, [monitorID]);

  if (items === null) return <Loading what="quedas" />;

  if (items.length === 0) {
    return (
      <Nothing hint="Oscilação passageira não entra aqui: uma queda só é registrada depois de confirmada pelo número de falhas seguidas do monitor.">
        Nenhuma queda confirmada.
      </Nothing>
    );
  }

  return (
    <ul className="border-t border-line">
      {items.map((i) => (
        <li key={i.id} className="flex items-baseline justify-between gap-4 border-b border-line py-2">
          <span className="flex min-w-0 items-baseline gap-3">
            <StatusDot status={i.open ? "down" : "up"} />
            <span className="tabular w-[72px] shrink-0 text-body">
              {i.open ? "em curso" : "resolvido"}
            </span>
            {i.cause && <span className="truncate text-small text-ink-2">{i.cause}</span>}
          </span>

          {/* Duração à direita numa coluna própria: durante uma conversa
              sobre disponibilidade é o número que se compara entre linhas. */}
          <span className="flex shrink-0 items-baseline gap-3 text-small">
            <span className="tabular w-[64px] text-right text-ink">
              {duration(i.duration_seconds)}
            </span>
            <span className="tabular text-ink-3">{stamp(i.started_at)}</span>
          </span>
        </li>
      ))}
    </ul>
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
      <Nothing hint="O estado se manteve do início ao fim da janela escolhida.">
        Nenhuma mudança no período.
      </Nothing>
    );
  }

  return (
    <ul className="border-t border-line">
      {transicoes.slice(0, 20).map((hb, i) => (
        <li
          key={hb.timestamp + i}
          className="flex items-baseline justify-between gap-4 border-b border-line py-2"
        >
          <span className="flex min-w-0 items-baseline gap-3">
            <StatusDot status={hb.status} withLabel />
            {hb.message && <span className="truncate text-body text-ink-2">{hb.message}</span>}
          </span>
          {/* Largura fixa no tempo relativo: sem ela "agora" e "há 11 min"
              empurram o horário absoluto para posições diferentes, e as
              duas colunas de tempo deixam de ser colunas. */}
          <span className="flex shrink-0 items-baseline gap-3 text-small text-ink-3">
            <span className="tabular">{stamp(hb.timestamp)}</span>
            <span className="tabular w-[68px] text-right">{ago(hb.timestamp)}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

