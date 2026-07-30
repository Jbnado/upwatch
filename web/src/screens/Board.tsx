import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { api } from "../api/client";
import type { Monitor, Status } from "../api/types";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import { Alert, Button, Empty, Loading, RowLink, StatusDot } from "../components/ui";
import { ago, latency, uptime } from "../lib/format";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { navigate, pool } from "../lib/router";
import { bucketStatus, isStale, summariseHeartbeats, summariseRollups } from "../lib/series";

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
  uptimePercent: number | null;
  p95: number | null;
};

export function Board({ podeEscrever }: { podeEscrever: boolean }) {
  const [range, setRange] = useState<Range>(DEFAULT_RANGE);
  const [readings, setReadings] = useState<Reading[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [agrupamento, setAgrupamento] = useState<string | null>(null);

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

  // As etiquetas vêm dos próprios monitores, não de um cadastro à parte:
  // uma lista separada exigiria manter dois lugares em dia, e etiqueta
  // sem alvo nenhum não tem o que agrupar.
  const etiquetas = useMemo(() => etiquetasDe(readings ?? []), [readings]);

  // O agrupamento escolhido some se a etiqueta deixar de existir —
  // acontece ao apagar o último monitor que a usava, e sem isto o painel
  // ficaria preso num grupo vazio.
  useEffect(() => {
    if (agrupamento && !etiquetas.includes(agrupamento)) setAgrupamento(null);
  }, [etiquetas, agrupamento]);

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-6">
      <header className="flex flex-wrap items-baseline justify-between gap-4">
        <Summary {...resumo} total={readings?.length ?? 0} />
        <div className="flex items-center gap-3">
          <RangePicker value={range} onChange={setRange} />
          {podeEscrever && (
            <Button variant="primary" onClick={() => navigate({ name: "monitor-new" })}>
              Novo monitor
            </Button>
          )}
        </div>
      </header>

      {/* O agrupamento só aparece quando há etiqueta cadastrada: numa
          instalação sem elas, o controle seria uma escolha entre uma
          opção só. */}
      {etiquetas.length > 0 && (
        <div className="-mt-2 flex flex-wrap items-center gap-2">
          <span className="eyebrow mr-1">agrupar por</span>
          <Chip ativo={agrupamento === null} onClick={() => setAgrupamento(null)}>
            nada
          </Chip>
          {etiquetas.map((t) => (
            <Chip key={t} ativo={agrupamento === t} onClick={() => setAgrupamento(t)}>
              {t}
            </Chip>
          ))}
        </div>
      )}

      {erro && <Alert>{erro}</Alert>}

      {readings === null ? (
        <Loading what="monitores" />
      ) : readings.length === 0 ? (
        <Empty
          title="Nenhum alvo sendo vigiado"
          description="Cadastre o primeiro serviço para começar a acompanhar disponibilidade e latência."
          action={
            podeEscrever ? (
              <Button variant="primary" onClick={() => navigate({ name: "monitor-new" })}>
                Cadastrar monitor
              </Button>
            ) : undefined
          }
        />
      ) : (
        <div>
          {/* Rótulos de coluna uma vez só. Repeti-los em cada linha
              encheria a lista de texto que não muda e competiria com os
              números, que são o que se lê. */}
          {/* O cabeçalho usa exatamente a mesma grade da linha, para
              rótulo e valor caírem na mesma coluna. */}
          <div className="flex items-center gap-4 border-y border-line px-1 py-1.5">
            <span className="eyebrow flex-1">alvo</span>
            {/* À direita, como as barras: a faixa cresce a partir de agora
                encostada na borda direita, e um rótulo à esquerda ficaria
                apontando para o espaço vazio do período ainda não medido. */}
            <span className="eyebrow w-[196px] shrink-0 text-right">
              {range.label} de histórico
            </span>
            <span className="grid w-[164px] shrink-0 grid-cols-2 gap-4 text-right">
              <span className="eyebrow">disponib.</span>
              <span className="eyebrow">p95</span>
            </span>
          </div>

          {agrupar(readings, agrupamento).map((grupo) => (
            <div key={grupo.nome}>
              {/* O cabeçalho do grupo só aparece quando há agrupamento:
                  numa instalação de um ambiente só, ele seria uma linha
                  de ruído em cima de cada bloco. */}
              {agrupamento && (
                <div className="flex items-baseline gap-3 border-b border-line px-1 pb-1.5 pt-4">
                  <span className="text-body font-medium">{grupo.nome}</span>
                  <span className="eyebrow">{grupo.leituras.length}</span>
                </div>
              )}
              {grupo.leituras.map((reading) => (
                <Row key={reading.monitor.id} reading={reading} range={range} />
              ))}
            </div>
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
function Row({ reading, range }: { reading: Reading; range: Range }) {
  const { monitor, samples, status, uptimePercent, p95 } = reading;

  const ultima = samples.at(-1);
  const stale = isStale(ultima?.at, monitor.interval_seconds, range.source);

  return (
    // Link, não botão. Um button só aceita conteúdo de frase, e a div da
    // faixa fazia o parser fechar o botão antes da hora — clicar sobre o
    // traço não navegava, sem nada indicar o motivo. Como link, também
    // funciona abrir em nova aba com Ctrl, que é o esperado numa lista.
    <RowLink
      to={{ name: "monitor", id: monitor.id }}
      className="flex items-center gap-4 border-b border-line px-1 py-2"
    >
      <span className="flex min-w-0 flex-1 items-baseline gap-2">
        <StatusDot status={status} />
        <span className="truncate text-body font-medium text-ink">{monitor.name}</span>
        <span className="tabular truncate text-small text-ink-3">{monitor.target}</span>
        {stale && (
          // Monitor que parou de ser verificado é uma falha própria, não
          // do alvo; sem este aviso ela passaria por "tudo estável".
          <span className="shrink-0 text-micro text-degraded">
            sem verificar {ago(samples.at(-1)!.at)}
          </span>
        )}
      </span>

      <span className="w-[196px] shrink-0">
        <Pulse samples={samples} rangeLabel={range.label} height={26} />
      </span>

      {/* Duas colunas de largura fixa, ambas alinhadas à direita: é assim
          que os dígitos se empilham e dá para comparar latências descendo
          a lista. Alinhar uma à esquerda quebraria justamente isso. */}
      <span className="tabular grid w-[164px] shrink-0 grid-cols-2 gap-4 whitespace-nowrap text-right text-body">
        <span className={uptimePercent === null ? "text-ink-3" : undefined}>
          {uptime(uptimePercent)}
        </span>
        <span className={p95 === null ? "text-ink-3" : undefined}>{latency(p95)}</span>
      </span>
    </RowLink>
  );
}

/**
 * Chip é um seletor de agrupamento.
 *
 * Fileira de fichas em vez de menu suspenso: são poucas opções, e trocar
 * de recorte é gesto frequente enquanto se investiga — cada clique a
 * mais é um clique dado com o serviço fora do ar.
 */
function Chip({
  ativo,
  onClick,
  children,
}: {
  ativo: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={ativo}
      onClick={onClick}
      className={[
        "pressable inline-flex h-[var(--control-h-sm)] items-center rounded-sm px-2.5 text-small",
        ativo
          ? "bg-ink text-paper"
          : "border border-line-strong text-ink-2 hover:bg-sunken hover:text-ink active:bg-pressed",
      ].join(" ")}
    >
      {children}
    </button>
  );
}

/**
 * etiquetasDe reúne as etiquetas em uso, ordenadas.
 *
 * O servidor já as devolve normalizadas, então não há o que unificar
 * aqui — só juntar e ordenar para a fileira ficar estável entre visitas.
 */
export function etiquetasDe(readings: { monitor: Monitor }[]): string[] {
  const todas = new Set<string>();
  for (const r of readings) {
    for (const t of r.monitor.tags ?? []) todas.add(t);
  }
  return [...todas].sort();
}

/**
 * agrupar reparte as leituras pela etiqueta escolhida.
 *
 * Um monitor pode ter várias etiquetas, mas o agrupamento é por uma só:
 * repetir a mesma linha em dois grupos faria as contagens do topo não
 * fecharem com o que está na tela. Quem não tem a etiqueta cai num grupo
 * final — some da lista seria pior, porque some sem avisar.
 */
export function agrupar<T extends { monitor: Monitor }>(
  readings: T[],
  etiqueta: string | null,
): { nome: string; leituras: T[] }[] {
  if (!etiqueta) return [{ nome: "", leituras: readings }];

  const dentro = readings.filter((r) => (r.monitor.tags ?? []).includes(etiqueta));
  const fora = readings.filter((r) => !(r.monitor.tags ?? []).includes(etiqueta));

  const grupos: { nome: string; leituras: T[] }[] = [];
  if (dentro.length > 0) grupos.push({ nome: etiqueta, leituras: dentro });
  if (fora.length > 0) grupos.push({ nome: `sem "${etiqueta}"`, leituras: fora });
  return grupos;
}

/**
 * Summary é o título da tela.
 *
 * É o próprio estado da frota, não a palavra "Painel": o que se quer
 * saber ao abrir a página é se há algo para resolver, e essa resposta
 * merece o lugar do cabeçalho. A marca já fica presa no topo, então
 * repeti-la aqui só gastaria a linha mais valiosa da tela.
 *
 * "Tudo no ar" fica quieto de propósito; o que exige ação aparece
 * primeiro e é o único elemento colorido.
 */
function Summary({
  up,
  degraded,
  down,
  unknown,
  total,
}: {
  up: number;
  degraded: number;
  down: number;
  unknown: number;
  total: number;
}) {
  const tudoBem = down === 0 && degraded === 0 && unknown === 0 && total > 0;

  if (total === 0) {
    return <h1 className="text-lead font-semibold tracking-tight text-ink">Monitores</h1>;
  }

  return (
    <h1 className="flex items-baseline gap-3 text-lead font-semibold tracking-tight">
      {tudoBem ? (
        <span className="text-ink">
          <span className="tabular">{up}</span> alvos, todos no ar
        </span>
      ) : (
        <>
          {down > 0 && (
            <span className="text-down">
              <span className="tabular">{down}</span> fora do ar
            </span>
          )}
          {degraded > 0 && (
            <span className="text-degraded">
              <span className="tabular">{degraded}</span> degradado{degraded > 1 ? "s" : ""}
            </span>
          )}
          {/* Sem medição precisa aparecer, e discreto: sem esta parcela as
              contagens não fecham com o total e o painel parece ter
              perdido monitores. Mas também não é alarme — é alvo que
              ainda não reportou. */}
          {unknown > 0 && (
            <span className="text-body font-normal text-unknown">
              <span className="tabular">{unknown}</span> sem medição
            </span>
          )}
          {up > 0 && (
            <span className="text-body font-normal text-ink-3">
              <span className="tabular">{up}</span> no ar
            </span>
          )}
        </>
      )}
    </h1>
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
    const { status, uptimePercent, p95 } = summariseHeartbeats(items);

    return {
      monitor,
      samples: items.map((hb) => ({
        at: hb.timestamp,
        status: hb.status,
        latencyMs: hb.latency_ms,
      })),
      status,
      uptimePercent,
      p95,
    };
  }

  const { items } = await api.rollups(monitor.id, {
    from,
    to,
    resolution: range.source === "hourly" ? "hourly" : "daily",
  });
  const { status, uptimePercent, p95 } = summariseRollups(items);

  return {
    monitor,
    samples: items.map((r) => ({
      at: r.bucket_start,
      status: bucketStatus(r.up, r.degraded, r.down),
      latencyMs: r.latency_p95_ms,
    })),
    status,
    uptimePercent,
    p95,
  };
}
