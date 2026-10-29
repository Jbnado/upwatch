import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { api } from "../api/client";
import type { AffectedBy, Monitor, Status, Summary as SummaryDTO } from "../api/types";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import {
  Alert,
  Button,
  Empty,
  Input,
  Loading,
  Nothing,
  RowLink,
  StatusDot,
  TextLink,
} from "../components/ui";
import { rotuloCalado } from "../lib/dependencies";
import { ago, latency, uptime } from "../lib/format";
import { byProblem, crumb, matches, shortName, type StateFilter } from "../lib/groups";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { navigate } from "../lib/router";
import { isStale } from "../lib/series";

/**
 * PONTOS_DA_FAIXA é a resolução pedida ao servidor.
 *
 * A faixa tem 196 px; mais pontos que isso desenhariam traços de menos de
 * um pixel. É o servidor que divide a janela nesse número de buckets, o
 * que garante que a figura e os números descrevam o mesmo período.
 */
const PONTOS_DA_FAIXA = 60;

/**
 * O painel em lista.
 *
 * Uma lista densa em vez de cartões: cartões com sombra e respiro são o
 * visual padrão de painel, e desperdiçam a tela justamente onde ela mais
 * serve — comparar dezenas de alvos de relance. Aqui cada linha é um
 * canal do instrumento.
 *
 * É a tela de um grupo e, numa instalação sem grupos, a tela inicial.
 * Os problemas vêm primeiro: em ordem de cadastro, um alvo vermelho no
 * meio de sessenta verdes some.
 */

type Reading = {
  monitor: Monitor;
  samples: PulseSample[];
  status: Status;
  uptimePercent: number | null;
  p95: number | null;
  /** Instante da última verificação, do servidor. */
  lastCheckAt?: string | null;
  /** O pai em queda que cala o monitor, quando há. */
  affectedBy?: AffectedBy;
};

/** O grupo que a lista mostra; id nulo é "Sem grupo". */
export type BoardGroup = { id: number | null; path: string };

export function Board({
  podeEscrever,
  group,
}: {
  podeEscrever: boolean;
  /** Ausente é a instalação inteira, como antes dos grupos. */
  group?: BoardGroup;
}) {
  const [range, setRange] = useState<Range>(DEFAULT_RANGE);
  const [readings, setReadings] = useState<Reading[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [agrupamento, setAgrupamento] = useState<string | null>(null);
  const [busca, setBusca] = useState("");
  const [estado, setEstado] = useState<StateFilter>("todos");

  const grupoID = group?.id;

  const carregar = useCallback(async () => {
    try {
      const { from, to } = windowFor(range);

      // Duas requisições para a lista inteira, e nenhum cálculo aqui. O
      // filtro do grupo é do servidor, que já inclui os ambientes de um
      // sistema; "Sem grupo" não tem id, e quem o vê alcança tudo.
      const [page, resumos] = await Promise.all([
        api.listMonitors({ limit: 500, group_id: grupoID ?? undefined }),
        api.summaries({ from, to, buckets: PONTOS_DA_FAIXA }),
      ]);

      const porID = new Map(resumos.items.map((s) => [s.monitor_id, s]));
      const monitores =
        grupoID === null ? page.items.filter((m) => m.group_id == null) : page.items;

      setReadings(monitores.map((m) => leitura(m, porID.get(m.id))));
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar os monitores.");
    }
  }, [range, grupoID]);

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
  const etiquetas = useMemo(() => etiquetasDe(readings ?? []), [readings]);

  // O agrupamento escolhido some se a etiqueta deixar de existir —
  // acontece ao apagar o último monitor que a usava, e sem isto o painel
  // ficaria preso num grupo vazio.
  useEffect(() => {
    if (agrupamento && !etiquetas.includes(agrupamento)) setAgrupamento(null);
  }, [etiquetas, agrupamento]);

  const ordenadas = useMemo(
    () =>
      byProblem(readings ?? [], (r) => ({
        status: r.status,
        name: r.monitor.name,
        paused: !r.monitor.enabled,
      })),
    [readings],
  );

  const visiveis = ordenadas.filter((r) =>
    matches(
      {
        name: r.monitor.name,
        target: r.monitor.target,
        status: r.status,
        paused: !r.monitor.enabled,
      },
      busca,
      estado,
    ),
  );

  const novo = () =>
    navigate({ name: "monitor-new", group: grupoID === null ? undefined : grupoID });

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-6">
      {group && (
        <nav>
          <TextLink onClick={() => navigate({ name: "board" })}>← painel</TextLink>
        </nav>
      )}

      <header className="flex flex-wrap items-end justify-between gap-4">
        {group ? (
          <div className="flex min-w-0 flex-col gap-1.5">
            <GroupTitle path={group.path} />
            <Summary {...resumo} total={readings?.length ?? 0} quiet />
          </div>
        ) : (
          <Summary {...resumo} total={readings?.length ?? 0} />
        )}
        <div className="flex flex-wrap items-center gap-3">
          <RangePicker value={range} onChange={setRange} />
          {podeEscrever && (
            <Button variant="primary" onClick={novo}>
              Novo monitor
            </Button>
          )}
        </div>
      </header>

      {erro && <Alert>{erro}</Alert>}

      {readings === null ? (
        <Loading what="monitores" />
      ) : readings.length === 0 ? (
        <Empty
          title={group ? "Nenhum monitor neste grupo" : "Nenhum alvo sendo vigiado"}
          description="Cadastre o primeiro serviço para começar a acompanhar disponibilidade e latência."
          action={
            podeEscrever ? (
              <Button variant="primary" onClick={novo}>
                Cadastrar monitor
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          <Filters
            busca={busca}
            onBusca={setBusca}
            estado={estado}
            onEstado={setEstado}
            contagem={resumo}
            total={readings.length}
          />

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

          {visiveis.length === 0 ? (
            <Nothing hint="Limpe a busca ou escolha outro estado.">
              Nenhum monitor com esse filtro.
            </Nothing>
          ) : (
            <div>
              {/* Rótulos de coluna uma vez só, na mesma grade da linha,
                  para rótulo e valor caírem na mesma coluna. */}
              <div className="flex items-center gap-4 border-y border-line px-1 py-1.5">
                <span className="eyebrow flex-1">alvo</span>
                {/* À direita, como as barras: a faixa cresce a partir de
                    agora encostada na borda direita. */}
                <span className="eyebrow hidden w-[196px] shrink-0 text-right sm:block">
                  {range.label} de histórico
                </span>
                <span className="grid w-[132px] shrink-0 grid-cols-2 gap-3 text-right sm:w-[164px] sm:gap-4">
                  <span className="eyebrow">disponib.</span>
                  <span className="eyebrow">p95</span>
                </span>
              </div>

              {agrupar(visiveis, agrupamento).map((bloco) => (
                <div key={bloco.nome}>
                  {agrupamento && (
                    <div className="flex items-baseline gap-3 border-b border-line px-1 pb-1.5 pt-4">
                      <span className="text-body font-medium">{bloco.nome}</span>
                      <span className="eyebrow">{bloco.leituras.length}</span>
                    </div>
                  )}
                  {bloco.leituras.map((reading) => (
                    <Row
                      key={reading.monitor.id}
                      reading={reading}
                      range={range}
                      groupPath={group?.path}
                    />
                  ))}
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}

/**
 * GroupTitle é o caminho do grupo como título: o sistema discreto, o
 * ambiente em destaque. É o que se lê primeiro ao chegar pelo cartão.
 */
function GroupTitle({ path }: { path: string }) {
  const partes = crumb(path).split(" · ");
  const ultimo = partes.pop();

  return (
    <h1 className="text-title font-semibold tracking-tight">
      {partes.map((p) => (
        <span key={p} className="text-ink-3">
          {p} ·{" "}
        </span>
      ))}
      <span className="text-ink">{ultimo}</span>
    </h1>
  );
}

const ESTADOS: { value: StateFilter; label: string; status?: Status }[] = [
  { value: "todos", label: "todos" },
  { value: "down", label: "fora do ar", status: "down" },
  { value: "degraded", label: "degradado", status: "degraded" },
  { value: "unknown", label: "sem medição", status: "unknown" },
  { value: "up", label: "no ar", status: "up" },
  { value: "pausado", label: "pausados" },
];

const DOT: Record<Status, string> = {
  up: "bg-up",
  degraded: "bg-degraded",
  down: "bg-down",
  unknown: "bg-unknown",
};

/**
 * Filters é a busca por nome e o recorte por estado.
 *
 * Cada ficha traz a contagem, e as de estado vazio somem: oferecer "fora
 * do ar 0" é convidar a um clique que só confirma o que a ficha já disse.
 */
function Filters({
  busca,
  onBusca,
  estado,
  onEstado,
  contagem,
  total,
}: {
  busca: string;
  onBusca: (v: string) => void;
  estado: StateFilter;
  onEstado: (v: StateFilter) => void;
  contagem: Record<Status | "paused", number>;
  total: number;
}) {
  const quantos = (v: StateFilter) =>
    v === "todos" ? total : v === "pausado" ? contagem.paused : contagem[v];

  const fichas = ESTADOS.filter(
    (e) => e.value === "todos" || quantos(e.value) > 0 || estado === e.value,
  );

  return (
    <div className="flex flex-wrap items-center gap-3">
      <Input
        type="search"
        value={busca}
        onChange={(e) => onBusca(e.target.value)}
        placeholder="buscar por nome ou alvo"
        aria-label="buscar por nome ou alvo"
        className="w-full sm:w-[240px]"
      />
      <div role="group" aria-label="filtrar por estado" className="flex flex-wrap gap-2">
        {fichas.map((e) => (
          <Chip key={e.value} ativo={estado === e.value} onClick={() => onEstado(e.value)}>
            {e.status && (
              <span
                className={`mr-1.5 h-[7px] w-[7px] rounded-full ${DOT[e.status]}`}
                aria-hidden
              />
            )}
            {e.label}
            <span className="tabular ml-1.5 opacity-70">{quantos(e.value)}</span>
          </Chip>
        ))}
      </div>
    </div>
  );
}

/**
 * Row é um canal do painel.
 *
 * Nome e alvo à esquerda, medidas alinhadas à direita em coluna tabular,
 * e a faixa ocupando a largura restante. Os números ficam na mesma
 * posição em toda linha, então o olho desce a coluna comparando em vez de
 * caçar. No celular a faixa e o alvo saem: não cabem sem rolar de lado, e
 * estado e números são o que se lê numa tela pequena.
 */
function Row({
  reading,
  range,
  groupPath,
}: {
  reading: Reading;
  range: Range;
  groupPath?: string;
}) {
  const { monitor, samples, status, uptimePercent, p95 } = reading;

  const stale = monitor.enabled && isStale(reading.lastCheckAt, monitor.interval_seconds);
  const nome = groupPath ? shortName(monitor.name, groupPath) : monitor.name;

  return (
    // Link, não botão: a faixa é uma div, e dentro de um button o parser
    // fecharia o botão antes da hora. Como link, também funciona abrir em
    // nova aba com Ctrl, que é o esperado numa lista.
    <RowLink
      to={{ name: "monitor", id: monitor.id }}
      className={`flex items-center gap-4 border-b border-line px-1 py-2 ${monitor.enabled ? "" : "opacity-60"}`}
    >
      <span className="flex min-w-0 flex-1 items-baseline gap-2">
        <StatusDot status={status} />
        <span className="truncate text-body font-medium text-ink" title={monitor.name}>
          {nome}
        </span>
        <span className="tabular hidden truncate text-small text-ink-3 sm:inline">
          {monitor.target}
        </span>
        {!monitor.enabled && <span className="eyebrow shrink-0">pausado</span>}
        {reading.affectedBy && (
          // A falha não é dele: o pai caiu. Cinza e dizendo quem, para não
          // parecer mais um vermelho a investigar.
          <span className="shrink-0 truncate text-micro text-unknown">
            {rotuloCalado(reading.affectedBy)}
          </span>
        )}
        {stale && (
          // Monitor que parou de ser verificado é uma falha própria, não
          // do alvo; sem este aviso ela passaria por "tudo estável".
          <span className="shrink-0 text-micro text-degraded">
            sem verificar {ago(reading.lastCheckAt!)}
          </span>
        )}
      </span>

      <span className="hidden w-[196px] shrink-0 sm:block">
        <Pulse samples={samples} rangeLabel={range.label} height={26} />
      </span>

      {/* Duas colunas de largura fixa, ambas alinhadas à direita: é assim
          que os dígitos se empilham e dá para comparar latências descendo
          a lista. */}
      <span className="tabular grid w-[132px] shrink-0 grid-cols-2 gap-3 whitespace-nowrap text-right text-body sm:w-[164px] sm:gap-4">
        <span className={uptimePercent === null ? "text-ink-3" : undefined}>
          {uptime(uptimePercent)}
        </span>
        <span className={p95 === null ? "text-ink-3" : undefined}>{latency(p95)}</span>
      </span>
    </RowLink>
  );
}

/**
 * Chip é uma ficha de escolha.
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
 * Summary é o estado da frota em uma linha.
 *
 * Na instalação sem grupos ele é o título da tela: o que se quer saber ao
 * abrir a página é se há algo para resolver. Na tela do grupo o título é
 * o grupo, e o resumo desce para baixo dele, mais discreto.
 *
 * "Tudo no ar" fica quieto de propósito; o que exige ação aparece
 * primeiro e é o único elemento colorido.
 */
export function Summary({
  up,
  degraded,
  down,
  unknown,
  paused = 0,
  total,
  quiet = false,
}: {
  up: number;
  degraded: number;
  down: number;
  unknown: number;
  paused?: number;
  total: number;
  quiet?: boolean;
}) {
  const tudoBem = down === 0 && degraded === 0 && unknown === 0 && total > paused;
  const Tag = quiet ? "p" : "h1";
  const tamanho = quiet ? "text-body" : "text-lead font-semibold tracking-tight";

  if (total === 0) {
    return quiet ? null : (
      <h1 className="text-lead font-semibold tracking-tight text-ink">Monitores</h1>
    );
  }

  return (
    <Tag className={`flex flex-wrap items-baseline gap-x-3 ${tamanho}`}>
      {tudoBem ? (
        <span className="text-ink">
          <span className="tabular">{up}</span> alvo{up === 1 ? "" : "s"}, todos no ar
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
              perdido monitores. */}
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
      {paused > 0 && (
        <span className="text-body font-normal text-ink-3">
          <span className="tabular">{paused}</span> pausado{paused > 1 ? "s" : ""}
        </span>
      )}
    </Tag>
  );
}

function summarise(readings: Reading[]) {
  return readings.reduce(
    (acc, r) =>
      r.monitor.enabled
        ? { ...acc, [r.status]: acc[r.status] + 1 }
        : { ...acc, paused: acc.paused + 1 },
    { up: 0, degraded: 0, down: 0, unknown: 0, paused: 0 } as Record<Status | "paused", number>,
  );
}

/**
 * leitura converte o resumo do servidor no que a linha desenha.
 *
 * Converte, não calcula. Monitor sem resumo é ausência, e não zero: um
 * alvo recém-criado ainda não disse nada sobre si.
 */
function leitura(monitor: Monitor, resumo: SummaryDTO | undefined): Reading {
  if (!resumo) {
    return { monitor, samples: [], status: "unknown", uptimePercent: null, p95: null };
  }

  return {
    monitor,
    samples: resumo.series.map((p) => ({
      at: p.at,
      status: p.status,
      latencyMs: p.latency_ms,
    })),
    // Calado pelo pai, o monitor não está sendo medido: sem medição, e não
    // o último estado confirmado, que diria "no ar" sem ninguém conferir.
    status: resumo.affected_by ? "unknown" : resumo.status,
    affectedBy: resumo.affected_by,
    uptimePercent: resumo.uptime_percent,
    p95: resumo.latency_p95_ms,
    lastCheckAt: resumo.last_check_at,
  };
}
