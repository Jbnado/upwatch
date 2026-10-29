import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import {
  ApiError,
  type AffectedBy,
  type Channel,
  type Group,
  type Incident,
  type Monitor,
  type MonitorEvent,
  type Problem,
  type Status,
  type Summary as SummaryDTO,
  type User,
} from "../api/types";
import { channelTypeLabel, MonitorChannels } from "./Channels";
import { Menu, type MenuItem } from "../components/Menu";
import { MonitorTree } from "../components/MonitorTree";
import { Pulse } from "../components/Pulse";
import type { PulseSample } from "../components/pulse-bars";
import { RangePicker } from "../components/RangePicker";
import { TabPanel, Tabs } from "../components/Tabs";
import {
  Alert,
  Button,
  InlineLink,
  Input,
  Loading,
  Nothing,
  StatusDot,
  TextLink,
} from "../components/ui";
import { duration, elapsed, latency, sinceClock, stamp, uptime } from "../lib/format";
import {
  consecutiveFailures,
  emptyTimeline,
  groupTimeline,
  timeline,
  type TimelineEntry,
  type TimelineLine,
} from "../lib/events";
import { explainCause } from "../lib/cause";
import { filhosDe, rotuloCalado, travasDeFilho } from "../lib/dependencies";
import { crumb } from "../lib/groups";
import { maskedPushAddress, pushAddress, pushSignal, pushTokenVisible } from "../lib/push";
import { DEFAULT_RANGE, windowFor, type Range } from "../lib/ranges";
import { vistoPor } from "../lib/reminders";
import { navigate, type MonitorTab } from "../lib/router";
import { NO_CHANNEL_LINK_HINT, canLinkChannels, channelOrigins } from "../lib/scope";

/** Mesma resolução de faixa do painel: as duas telas desenham igual. */
const PONTOS_DA_FAIXA = 60;

/**
 * SEQUENCIA é quantas batidas se leem para contar as falhas seguidas.
 * Passando disso, a tela diz "mais de" em vez de um número exato: depois
 * de mil falhas, a contagem precisa não muda decisão nenhuma.
 */
const SEQUENCIA = 1000;

/** Falhas vizinhas mostradas de cada lado da falha do link num grupo. */
const VIZINHAS = 2;

/** Eventos por página da linha do tempo. */
const POR_PAGINA = 50;

/**
 * Detalhe de um alvo.
 *
 * No topo só o que importa agora: o quadro de estado aparece apenas com
 * problema, e números e faixa ficam numa linha compacta. O resto — linha
 * do tempo, quedas e quem é avisado — mora atrás de abas, com Eventos na
 * frente, porque a pergunta de quem chega aqui é "o que aconteceu?".
 */
export function MonitorDetail({
  id,
  tab,
  anchor,
  eu,
}: {
  id: number;
  tab: MonitorTab;
  anchor?: string;
  eu: User;
}) {
  const [range, setRange] = useState<Range>(DEFAULT_RANGE);
  const [monitor, setMonitor] = useState<Monitor | null>(null);
  const [resumo, setResumo] = useState<SummaryDTO | null>(null);
  const [grupos, setGrupos] = useState<Group[]>([]);
  const [problema, setProblema] = useState<Problem | null>(null);
  const [sequencia, setSequencia] = useState<number | null>(null);
  const [cursor, setCursor] = useState<PulseSample | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [rodada, setRodada] = useState(0);
  const [mostrarToken, setMostrarToken] = useState(false);
  const [monitores, setMonitores] = useState<Monitor[]>([]);
  const [ligando, setLigando] = useState(false);

  const administra = eu.role === "admin";

  const carregar = useCallback(async () => {
    try {
      const { from, to } = windowFor(range);

      // Mesma fonte do painel, e nenhum cálculo aqui. O problema vem da
      // mesma lista da tela inicial, para o "há quanto tempo" e a causa
      // baterem com o que levou a pessoa até aqui.
      const [m, resumos, g, agora] = await Promise.all([
        api.getMonitor(id),
        api.summaries({ from, to, buckets: PONTOS_DA_FAIXA }),
        api.listGroups(),
        api.problems(),
      ]);

      const p = agora.items.find((x) => x.monitor_id === id) ?? null;
      setMonitor(m);
      setResumo(resumos.items.find((s) => s.monitor_id === id) ?? null);
      setGrupos(g.items);
      setProblema(p);
      setErro(null);
      setRodada((r) => r + 1);

      // Pai e dependentes saem da lista de monitores. À parte do resto:
      // sem ela a tela continua servindo, só sem a seção de dependências.
      void api
        .listMonitors({ limit: 500 })
        .then((r) => setMonitores(r.items))
        .catch(() => setMonitores([]));

      if (p && p.status !== "unknown") {
        // A sequência pode ter começado antes de o estado ser confirmado:
        // o limiar pede algumas falhas antes de declarar a queda.
        const folga = (m.confirmation_threshold + 2) * m.interval_seconds * 1000;
        const desde = new Date(new Date(p.since).getTime() - folga).toISOString();
        const batidas = await api.heartbeats(id, { from: desde, limit: SEQUENCIA });
        setSequencia(consecutiveFailures(batidas.items));
      } else {
        setSequencia(null);
      }
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

  if (erro && !monitor) {
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
    resumo?.series.map((p) => ({ at: p.at, status: p.status, latencyMs: p.latency_ms })) ?? [];

  // Calado pelo pai, o monitor não está sendo medido: a falha não é dele,
  // e o estado dele é "sem medição", como no painel.
  const calado = monitor.enabled ? resumo?.affected_by : undefined;
  // O estado de agora vem do problema quando há um: é a mesma fonte da
  // tela inicial. Sem problema, o resumo diz o estado confirmado.
  const atual: Status = calado ? "unknown" : (problema?.status ?? resumo?.status ?? "unknown");
  // A falha que ainda não virou queda só aparece na última batida: o
  // estado confirmado continua "no ar" até o limiar. Sem medição não
  // entra — é a rede do próprio servidor, não o alvo.
  const ultima = resumo?.last_check_status;
  const pendente: Exclude<Status, "up" | "unknown"> | null =
    atual === "up" && (ultima === "down" || ultima === "degraded") ? ultima : null;
  const grupo = grupos.find((g) => g.id === monitor.group_id);
  const sistema = grupo?.parent_id ? grupos.find((g) => g.id === grupo.parent_id) : undefined;
  const voltar = monitor.group_id != null ? { name: "group" as const, id: monitor.group_id } : null;

  async function remover() {
    if (!confirm(`Remover "${monitor!.name}" e todo o seu histórico?`)) return;

    await api.deleteMonitor(monitor!.id);
    navigate(voltar ?? { name: "board" });
  }

  const token = monitor.type === "push" ? String(monitor.config?.["token"] ?? "") : "";

  const menu: MenuItem[] = (["csv", "json"] as const).map((formato) => ({
    kind: "link",
    label: `Exportar ${formato.toUpperCase()} (${range.label})`,
    href: api.exportUrl(monitor.id, { ...windowFor(range), format: formato }),
  }));
  // O token inteiro é a credencial do monitor: só aparece a quem
  // administra, e só por pedido.
  if (administra && pushTokenVisible(token)) {
    menu.push({
      kind: "action",
      label: mostrarToken ? "Ocultar token" : "Mostrar token",
      onSelect: () => setMostrarToken((v) => !v),
    });
  }
  const pai = monitor.parent_id != null ? monitores.find((m) => m.id === monitor.parent_id) : undefined;
  const filhos = filhosDe(monitor.id, monitores);
  // Com dependentes, a seção já está à vista com o próprio botão; o menu
  // só serve para o primeiro.
  if (administra && filhos.length === 0) {
    menu.push({ kind: "action", label: "Ligar dependentes", onSelect: () => setLigando(true) });
  }
  if (administra) {
    menu.push({ kind: "action", label: "Remover monitor", onSelect: remover, danger: true });
  }

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-6 px-5 py-6">
      <nav>
        <TextLink onClick={() => navigate(voltar ?? { name: "board" })}>
          ← {grupo ? crumb(grupo.path) : "painel"}
        </TextLink>
      </nav>

      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex min-w-0 flex-col gap-1.5">
          <div className="flex items-center gap-2">
            {/* Só o ponto: o estado por extenso está no quadro logo abaixo,
                e "fora do ar bff" na frente do nome leria como uma frase. */}
            <StatusDot status={atual} />
            <h1 className="truncate text-title font-semibold tracking-tight">{monitor.name}</h1>
            {!monitor.enabled && <span className="eyebrow">pausado</span>}
          </div>
          <p className="tabular break-words text-body text-ink-3">
            {monitor.type} · <span className="break-all">{monitor.target || "recebe sinal do próprio serviço"}</span>
            {" · "}
            <span className="whitespace-nowrap">a cada {duration(monitor.interval_seconds)}</span>
            {pai && (
              <>
                {" · "}
                <span className="whitespace-nowrap">
                  depende de <InlineLink to={{ name: "monitor", id: pai.id }}>{pai.name}</InlineLink>
                </span>
              </>
            )}
          </p>
        </div>

        <div className="flex items-center gap-2">
          {administra && (
            <Button onClick={() => navigate({ name: "monitor-edit", id: monitor.id })}>
              Editar
            </Button>
          )}
          <Menu label="Mais ações" items={menu} />
        </div>
      </header>

      {erro && <Alert>{erro}</Alert>}

      {monitor.type === "push" && (
        <PushInfo
          status={atual}
          token={token}
          reveal={mostrarToken}
          nextExpected={monitor.next_expected_push}
          timezone={monitor.config?.["timezone"] ? String(monitor.config["timezone"]) : undefined}
        />
      )}

      {calado && <SilencedBox por={calado} />}
      {monitor.enabled && !calado && atual !== "up" && (
        <StateBox status={atual} problem={problema} streak={sequencia} type={monitor.type} />
      )}
      {monitor.enabled && !calado && atual === "down" && (
        <Acknowledge monitorID={monitor.id} administra={administra} rodada={rodada} />
      )}
      {monitor.enabled && !calado && pendente && (
        <StateBox status={pendente} problem={null} streak={null} type={monitor.type} pending />
      )}

      <section aria-label="medidas" className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <Numbers resumo={resumo} cursor={cursor} />
          <RangePicker value={range} onChange={setRange} />
        </div>
        <Pulse
          samples={samples}
          onInspect={setCursor}
          rangeLabel={range.label}
          height={56}
          maxBarWidth={12}
          baseline
        />
      </section>

      {(filhos.length > 0 || ligando) && (
        <Dependents
          monitor={monitor}
          monitores={monitores}
          grupos={grupos}
          filhos={filhos}
          administra={administra}
          ligando={administra && ligando}
          onLigar={() => setLigando(true)}
          onFechar={() => setLigando(false)}
          onMudou={() => void carregar()}
        />
      )}

      <div>
        <Tabs<MonitorTab>
          id="monitor"
          label="Histórico do monitor"
          value={tab}
          onChange={(t) => navigate({ name: "monitor", id: monitor.id, tab: t }, { replace: true })}
          tabs={[
            { key: "eventos", label: "Eventos" },
            { key: "quedas", label: "Quedas" },
            { key: "avisos", label: "Avisos" },
          ]}
        />
        <TabPanel id="monitor" tab={tab}>
          {tab === "eventos" && (
            <Events monitorID={monitor.id} status={atual} anchor={anchor} rodada={rodada} />
          )}
          {tab === "quedas" && <Incidents monitorID={monitor.id} />}
          {tab === "avisos" && (
            <Recipients
              monitor={monitor}
              grupo={grupo}
              sistema={sistema}
              administra={administra}
              vincula={administra && canLinkChannels(eu.scope)}
            />
          )}
        </TabPanel>
      </div>
    </div>
  );
}

const BOX_TONE: Record<Exclude<Status, "up">, { box: string; text: string; word: string }> = {
  down: { box: "border-down/50 bg-down-dim", text: "text-down", word: "fora do ar" },
  degraded: { box: "border-degraded/50 bg-degraded-dim", text: "text-degraded", word: "lento" },
  unknown: { box: "border-line-strong bg-sunken", text: "text-ink-2", word: "sem medição" },
};

/**
 * StateBox é o quadro de estado, e só existe com problema.
 *
 * Há quanto tempo, a causa e desde quando — as três perguntas de quem
 * abre um alvo fora do ar. No ar, o quadro some: a boa notícia não merece
 * o maior elemento da tela.
 */
function StateBox({
  status,
  problem,
  streak,
  type,
  pending = false,
}: {
  status: Exclude<Status, "up">;
  problem: Problem | null;
  streak: number | null;
  type: Monitor["type"];
  /** Só a última verificação falhou; o estado confirmado ainda é no ar. */
  pending?: boolean;
}) {
  const tom = BOX_TONE[status];
  // Antes da confirmação, "fora do ar" afirmaria a queda que o servidor
  // ainda não declarou; lento continua lento.
  const palavra =
    pending && status === "down"
      ? "falhando"
      : type === "push" && status === "down"
        ? "sem sinal"
        : tom.word;

  const rodape: string[] = [];
  if (streak !== null && streak > 0) {
    const n = streak >= SEQUENCIA ? `mais de ${SEQUENCIA}` : String(streak);
    rodape.push(
      status === "degraded"
        ? `${n} verificaç${streak === 1 ? "ão lenta seguida" : "ões lentas seguidas"}`
        : `${n} falha${streak === 1 ? "" : "s"} seguida${streak === 1 ? "" : "s"}`,
    );
  }
  if (problem) rodape.push(`desde ${sinceClock(problem.since)}`);
  // Sem problema na lista, a última verificação falhou mas o limiar de
  // confirmação ainda não foi atingido: dizer "há X" seria afirmar uma
  // queda que o servidor ainda não declarou.
  else rodape.push("falhando, ainda não confirmado como queda");

  return (
    <section
      aria-label="estado atual"
      className={`flex flex-col gap-1 rounded-md border px-4 py-3 ${tom.box}`}
    >
      <p className={`text-small font-semibold uppercase tracking-wide ${tom.text}`}>
        {palavra}
        {problem && <> há {elapsed(problem.since)}</>}
      </p>
      {problem?.cause && <BoxCause raw={problem.cause} />}
      {rodape.length > 0 && <p className="tabular text-small text-ink-2">{rodape.join(" · ")}</p>}
    </section>
  );
}

/**
 * Acknowledge é o "estou vendo" da queda aberta.
 *
 * Fica logo abaixo do quadro vermelho porque é a primeira coisa a fazer
 * ao chegar: dizer aos outros que alguém já está olhando, e parar os
 * lembretes. Depois de reconhecida, a queda mostra quem e há quanto
 * tempo — é o que impede a segunda pessoa de mexer no mesmo problema sem
 * saber.
 */
function Acknowledge({
  monitorID,
  administra,
  rodada,
}: {
  monitorID: number;
  administra: boolean;
  rodada: number;
}) {
  const [aberto, setAberto] = useState<Incident | null>(null);
  const [nota, setNota] = useState("");
  const [enviando, setEnviando] = useState(false);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const page = await api.incidents({ monitor_id: monitorID, open: "true", limit: 1 });
    setAberto(page.items[0] ?? null);
  }, [monitorID]);

  useEffect(() => {
    void carregar().catch(() => setAberto(null));
  }, [carregar, rodada]);

  if (!aberto) return null;

  async function reconhecer(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    try {
      setAberto(await api.acknowledgeIncident(monitorID, aberto!.id, nota.trim() || undefined));
    } catch (e) {
      // Outra pessoa pode ter reconhecido no meio: a recusa diz quem, e a
      // releitura mostra o reconhecimento dela no lugar do botão.
      setErro(e instanceof ApiError ? e.message : "Não foi possível registrar.");
      await carregar().catch(() => undefined);
    } finally {
      setEnviando(false);
    }
  }

  const ack = aberto.acknowledgement;
  if (ack) {
    return (
      <section aria-label="reconhecimento" className="flex flex-col gap-0.5 border-l-2 border-line-strong pl-3">
        <p className="text-small text-ink">{vistoPor(ack)}</p>
        {ack.note && <p className="text-small text-ink-2">{ack.note}</p>}
        {erro && <p className="text-small text-ink-3">{erro}</p>}
      </section>
    );
  }

  if (!administra) {
    return (
      <p className="text-small text-ink-3">Ninguém disse ainda que está olhando esta queda.</p>
    );
  }

  return (
    <form
      onSubmit={reconhecer}
      aria-label="reconhecimento"
      className="flex flex-wrap items-center gap-2"
    >
      <Input
        aria-label="nota do reconhecimento"
        placeholder="nota (opcional): o que você está fazendo"
        value={nota}
        maxLength={500}
        onChange={(e) => setNota(e.target.value)}
        className="min-w-0 flex-1"
      />
      <Button type="submit" variant="primary" disabled={enviando}>
        {enviando ? "Registrando" : "Estou vendo"}
      </Button>
      <span className="w-full text-small text-ink-3">
        Para os lembretes desta queda; o aviso de volta continua saindo.
      </span>
      {erro && <span className="w-full text-small text-down">{erro}</span>}
    </form>
  );
}

/**
 * SilencedBox é o quadro do monitor calado pelo pai.
 *
 * Cinza, e não vermelho: a falha não é dele. Caso real: a VM de uma sonda
 * perdeu a saída, e cada verificação dela aparecia fora do ar como se o
 * serviço tivesse caído junto.
 */
function SilencedBox({ por }: { por: AffectedBy }) {
  return (
    <section
      aria-label="estado atual"
      className="flex flex-col gap-1 rounded-md border border-line-strong bg-sunken px-4 py-3"
    >
      <p className="text-small font-semibold uppercase tracking-wide text-ink-2">{rotuloCalado(por)}</p>
      <p className="text-small text-ink-2">
        {por.id != null && por.name ? (
          <>
            Enquanto <InlineLink to={{ name: "monitor", id: por.id }}>{por.name}</InlineLink> estiver
            sem responder,
          </>
        ) : (
          "Enquanto a dependência estiver sem responder,"
        )}{" "}
        as falhas deste monitor não avisam e ficam como lacuna no histórico. Quando ela voltar, uma
        falha que continuar avisa normalmente.
      </p>
    </section>
  );
}

/**
 * Dependents lista quem depende deste monitor e, para quem administra,
 * liga e desliga ali mesmo, sem passar pelo menu.
 *
 * Ligar é em lote, pela árvore de grupos, porque o caso comum é ligar as
 * verificações de uma sonda ao sinal de vida dela, e editar uma a uma é
 * onde se esquece alguma.
 */
function Dependents({
  monitor,
  monitores,
  grupos,
  filhos,
  administra,
  ligando,
  onLigar,
  onFechar,
  onMudou,
}: {
  monitor: Monitor;
  monitores: Monitor[];
  grupos: Group[];
  filhos: Monitor[];
  administra: boolean;
  ligando: boolean;
  onLigar: () => void;
  onFechar: () => void;
  onMudou: () => void;
}) {
  const [escolhidos, setEscolhidos] = useState<Set<number>>(new Set());
  const [enviando, setEnviando] = useState(false);
  const [erro, setErro] = useState<string | null>(null);

  async function ligar() {
    setEnviando(true);
    setErro(null);
    try {
      // Na ordem da lista, e não na dos cliques: o pedido fica previsível.
      await api.setChildren(
        monitor.id,
        monitores.filter((m) => escolhidos.has(m.id)).map((m) => m.id),
      );
      setEscolhidos(new Set());
      onFechar();
      onMudou();
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível ligar os monitores.");
    } finally {
      setEnviando(false);
    }
  }

  async function desligar(filho: Monitor) {
    setErro(null);
    try {
      await api.unlinkChild(monitor.id, filho.id);
      onMudou();
    } catch (e) {
      setErro(e instanceof Error ? e.message : `Não foi possível desligar ${filho.name}.`);
    }
  }

  return (
    <section aria-label="dependentes" className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="eyebrow">dependentes</h2>
        {administra && !ligando && <TextLink onClick={onLigar}>Ligar dependentes</TextLink>}
      </div>
      {erro && <Alert>{erro}</Alert>}

      {filhos.length > 0 ? (
        <>
          <ul className="flex flex-col">
            {filhos.map((f) => (
              <li key={f.id} className="flex items-baseline gap-3 border-b border-line py-1.5">
                <span className="min-w-0 flex-1 truncate text-body">
                  <InlineLink to={{ name: "monitor", id: f.id }}>{f.name}</InlineLink>
                </span>
                {administra && (
                  <TextLink onClick={() => void desligar(f)} aria-label={`Desligar ${f.name}`}>
                    desligar
                  </TextLink>
                )}
              </li>
            ))}
          </ul>
          <p className="text-small text-ink-3">Com este fora do ar, eles não avisam.</p>
        </>
      ) : (
        <p className="text-small text-ink-3">Nenhum monitor depende deste ainda.</p>
      )}

      {ligando && (
        <div className="flex flex-col gap-2 rounded-md border border-line-strong px-3 py-3">
          <MonitorTree
            groups={grupos}
            monitors={monitores}
            selected={escolhidos}
            onChange={setEscolhidos}
            locked={travasDeFilho(monitor.id, monitores)}
            note="Marcar um grupo liga os monitores que estão nele agora. Um monitor criado depois no grupo não vira dependente sozinho."
          />
          <div className="flex gap-2">
            <Button
              variant="primary"
              disabled={escolhidos.size === 0 || enviando}
              onClick={() => void ligar()}
            >
              {escolhidos.size === 1 ? "Ligar 1 monitor" : `Ligar ${escolhidos.size} monitores`}
            </Button>
            <Button onClick={onFechar}>Cancelar</Button>
          </div>
        </div>
      )}
    </section>
  );
}

/**
 * BoxCause é a causa no quadro: o resumo em destaque e, quando houve
 * tradução, o texto técnico logo abaixo, discreto e cortado, inteiro a um
 * clique. O resumo responde "o que houve"; o técnico vai para o chamado.
 */
function BoxCause({ raw }: { raw: string }) {
  const [inteiro, setInteiro] = useState(false);
  const causa = explainCause(raw);

  return (
    <>
      <p className="break-words text-lead font-medium text-ink">{causa.summary}</p>
      {causa.known && (
        <p className="flex min-w-0 items-baseline gap-2 text-small text-ink-3">
          <span className={`tabular min-w-0 ${inteiro ? "[overflow-wrap:anywhere]" : "truncate"}`} title={raw}>
            {raw}
          </span>
          <button
            type="button"
            aria-expanded={inteiro}
            onClick={() => setInteiro((v) => !v)}
            className="pressable text-action shrink-0 rounded-sm underline decoration-line-strong hover:decoration-ink"
          >
            {inteiro ? "recolher" : "texto completo"}
          </button>
        </p>
      )}
    </>
  );
}

/**
 * Numbers é a linha compacta de medidas.
 *
 * Disponibilidade e p95 à vista; p50 e p99 aparecem ao passar o mouse ou
 * focar, e ficam sempre ditos ao leitor de tela. Com o cursor sobre a
 * faixa, a mesma linha vira o mostrador do ponto inspecionado.
 */
function Numbers({ resumo, cursor }: { resumo: SummaryDTO | null; cursor: PulseSample | null }) {
  if (cursor) {
    return (
      <div className="flex h-[var(--control-h)] items-center gap-4">
        <span className="tabular text-small text-ink-2">{stamp(cursor.at)}</span>
        <StatusDot status={cursor.status} withLabel />
        <span className="tabular text-small text-ink-2">{latency(cursor.latencyMs)}</span>
      </div>
    );
  }

  const p50 = latency(resumo?.latency_p50_ms ?? null);
  const p99 = latency(resumo?.latency_p99_ms ?? null);

  return (
    <div
      tabIndex={0}
      className="group flex h-[var(--control-h)] flex-wrap items-baseline gap-x-5 rounded-sm"
    >
      <Stat value={uptime(resumo?.uptime_percent ?? null)} label="disponib." />
      <Stat value={latency(resumo?.latency_p95_ms ?? null)} label="p95" />
      <span
        aria-hidden
        className="tabular hidden text-small text-ink-3 group-hover:inline group-focus-visible:inline"
      >
        p50 {p50} · p99 {p99}
      </span>
      <span className="sr-only">
        p50 {p50}, p99 {p99}
      </span>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <span className="flex items-baseline gap-1.5">
      <span className="tabular text-lead font-medium tracking-tight">{value}</span>
      <span className="eyebrow">{label}</span>
    </span>
  );
}

/**
 * PushInfo é o que um monitor push tem de próprio: como anda o sinal e o
 * endereço para onde o serviço bate, com o segredo cortado na tela e
 * inteiro na cópia. Quem só lê recebe o token mascarado pelo servidor, e
 * vê o sinal sem o endereço.
 */
function PushInfo({
  status,
  token,
  reveal,
  nextExpected,
  timezone,
}: {
  status: Status;
  token: string;
  reveal: boolean;
  nextExpected?: string;
  /** Fuso da agenda, para a próxima execução sair nele. */
  timezone?: string;
}) {
  const [copiado, setCopiado] = useState(false);
  const origem = window.location.origin;

  async function copiar() {
    try {
      await navigator.clipboard.writeText(pushAddress(origem, token));
      setCopiado(true);
      setTimeout(() => setCopiado(false), 2000);
    } catch {
      prompt("Copie o endereço:", pushAddress(origem, token));
    }
  }

  return (
    <section aria-label="sinal do push" className="flex flex-col gap-2">
      <p className="text-body text-ink-2">
        {pushSignal({ status, nextExpected, timezone }).join(" · ")}
      </p>
      {pushTokenVisible(token) && (
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <code className="min-w-0 max-w-full truncate rounded-sm border border-line-strong bg-surface px-2.5 py-1 text-small">
            {reveal ? pushAddress(origem, token) : maskedPushAddress(origem, token)}
          </code>
          <Button size="sm" onClick={copiar}>
            {copiado ? "Copiado" : "Copiar endereço"}
          </Button>
          <span aria-live="polite" className="sr-only">
            {copiado ? "endereço copiado" : ""}
          </span>
        </div>
      )}
    </section>
  );
}

const GLYPH_TONE: Record<Status, string> = {
  down: "text-down",
  degraded: "text-degraded",
  unknown: "text-unknown",
  up: "text-up",
};

const HORA_SEG = new Intl.DateTimeFormat("pt-BR", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});

/**
 * Events é a linha do tempo: falhas e mudanças de estado, cada uma com a
 * causa, da mais recente para trás.
 *
 * Vindo de um problema, a falha da âncora aparece em destaque e rolada
 * até a vista — o link colado no chat abre exatamente nela.
 */
function Events({
  monitorID,
  status,
  anchor,
  rodada,
}: {
  monitorID: number;
  status: Status;
  anchor?: string;
  rodada: number;
}) {
  const [filtro, setFiltro] = useState<"all" | "failure">("all");
  const [itens, setItens] = useState<MonitorEvent[] | null>(null);
  const [proxima, setProxima] = useState<string | undefined>();
  const [paginas, setPaginas] = useState(1);
  const [carregando, setCarregando] = useState(false);
  // A falha do link já nasce aberta: se ela estiver num grupo, o grupo
  // abre com ela marcada.
  const [abertos, setAbertos] = useState<Set<string>>(() => new Set(anchor ? [anchor] : []));

  // Recarrega a primeira página a cada rodada do detalhe, mas só enquanto
  // a pessoa não foi buscar páginas antigas: trocar o que ela está lendo
  // por baixo dela seria pior que esperar.
  useEffect(() => {
    if (paginas > 1) return;
    let vivo = true;
    void api
      .events(monitorID, { limit: POR_PAGINA, type: filtro === "failure" ? "failure" : undefined })
      .then((page) => {
        if (!vivo) return;
        setItens(page.items);
        setProxima(page.has_more ? page.next_cursor : undefined);
      });
    return () => {
      vivo = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [monitorID, filtro, rodada]);

  useEffect(() => {
    if (anchor) setAbertos((atual) => (atual.has(anchor) ? atual : new Set(atual).add(anchor)));
  }, [anchor]);

  useEffect(() => {
    if (!anchor || !itens) return;
    const alvo = document.getElementById(anchor);
    alvo?.scrollIntoView?.({ block: "center" });
  }, [anchor, itens]);

  async function maisAntigos() {
    if (!proxima) return;
    setCarregando(true);
    try {
      const page = await api.events(monitorID, {
        cursor: proxima,
        limit: POR_PAGINA,
        type: filtro === "failure" ? "failure" : undefined,
      });
      setItens((atual) => [...(atual ?? []), ...page.items]);
      setProxima(page.has_more ? page.next_cursor : undefined);
      setPaginas((n) => n + 1);
    } finally {
      setCarregando(false);
    }
  }

  function trocar(f: "all" | "failure") {
    setPaginas(1);
    setItens(null);
    setFiltro(f);
  }

  const linhas = itens ? timeline(itens) : [];
  const entradas = groupTimeline(linhas);
  const achou = !anchor || linhas.some((l) => l.anchor === anchor);
  const hoje = new Date().toDateString();
  const ehHoje = (iso: string) => new Date(iso).toDateString() === hoje;

  // Abertura guardada pelas âncoras das falhas, não por posição: o grupo
  // cresce a cada atualização e a cada página, e continua aberto.
  const alternar = (ancoras: string[], abrir: boolean) =>
    setAbertos((atual) => {
      const proximo = new Set(atual);
      for (const a of ancoras) {
        if (abrir) proximo.add(a);
        else proximo.delete(a);
      }
      return proximo;
    });

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className="eyebrow">falhas e mudanças de estado</span>
        <div
          role="group"
          aria-label="Quais eventos mostrar"
          className="inline-flex h-[var(--control-h-sm)] overflow-hidden rounded-sm border border-line-strong"
        >
          {(
            [
              ["all", "tudo"],
              ["failure", "só falhas"],
            ] as const
          ).map(([valor, rotulo], i) => (
            <button
              key={valor}
              type="button"
              aria-pressed={filtro === valor}
              onClick={() => trocar(valor)}
              className={[
                "pressable inline-flex items-center px-2.5 text-small",
                i > 0 ? "border-l border-line-strong" : "",
                filtro === valor
                  ? "bg-ink text-paper"
                  : "text-ink-2 hover:bg-sunken hover:text-ink active:bg-pressed",
              ].join(" ")}
            >
              {rotulo}
            </button>
          ))}
        </div>
      </div>

      {itens === null ? (
        <Loading what="eventos" />
      ) : linhas.length === 0 ? (
        <Nothing hint={emptyTimeline(status, filtro).hint}>{emptyTimeline(status, filtro).title}</Nothing>
      ) : (
        <>
          {!achou && (
            <p className="text-small text-ink-3">
              O evento do link não está entre os mais recentes; carregue os mais antigos para
              procurá-lo.
            </p>
          )}
          <ol className="border-t border-line">
            {entradas.map((e) =>
              e.kind === "line" ? (
                <EventRow
                  key={e.line.anchor + e.line.event.probe_id}
                  line={e.line}
                  destaque={e.line.anchor === anchor}
                  hoje={ehHoje(e.line.event.at)}
                  aberto={abertos.has(e.line.anchor)}
                  onToggle={(abrir) => alternar([e.line.anchor], abrir)}
                />
              ) : (
                <EventGroup
                  key={e.lines[e.lines.length - 1]!.anchor}
                  group={e}
                  anchor={anchor}
                  ehHoje={ehHoje}
                  aberto={e.lines.some((l) => abertos.has(l.anchor))}
                  onToggle={(abrir) => alternar(e.lines.map((l) => l.anchor), abrir)}
                />
              ),
            )}
          </ol>
          {proxima && (
            <Button onClick={maisAntigos} disabled={carregando} className="self-start">
              {carregando ? "Carregando" : "Carregar mais antigos"}
            </Button>
          )}
        </>
      )}
    </>
  );
}

/** Classe da linha; a borda existe sempre para o destaque não deslocar a coluna. */
function linhaClasse(destaque: boolean) {
  return [
    "flex items-baseline gap-3 border-b border-l-2 border-b-line py-2 pl-2 pr-1",
    destaque ? "border-l-down bg-down-dim" : "border-l-transparent",
  ].join(" ");
}

function Hora({ iso, hoje }: { iso: string; hoje: boolean }) {
  return (
    <time dateTime={iso} className="tabular w-[64px] shrink-0 text-small text-ink-3 sm:w-[112px]">
      {hoje ? HORA_SEG.format(new Date(iso)) : stamp(iso)}
    </time>
  );
}

function Glifo({ status, glyph }: { status: Status; glyph: string }) {
  return (
    <span aria-hidden className={`w-3 shrink-0 text-center text-small font-bold ${GLYPH_TONE[status]}`}>
      {glyph}
    </span>
  );
}

function Tecnico({ texto }: { texto: string }) {
  return <span className="tabular mt-1 block text-small text-ink-3 [overflow-wrap:anywhere]">{texto}</span>;
}

/**
 * EventRow é um evento solto. A falha com causa reconhecida mostra o
 * resumo e abre, num clique, o texto técnico inteiro — é ele que vai para
 * o chamado.
 */
function EventRow({
  line,
  destaque,
  hoje,
  aberto,
  onToggle,
}: {
  line: TimelineLine;
  destaque: boolean;
  hoje: boolean;
  aberto: boolean;
  onToggle: (abrir: boolean) => void;
}) {
  const { event } = line;
  const mudanca = event.type === "state";
  const tecnico = !mudanca && line.cause?.known ? line.cause.raw : undefined;

  const texto = (
    <>
      {!mudanca && <span className="sr-only">falha: </span>}
      <span className={`text-body ${mudanca ? "font-medium text-ink" : "text-ink-2"}`}>{line.label}</span>
      {line.detail && (
        <span className="text-body text-ink-2" title={line.cause?.known ? line.cause.raw : undefined}>
          : {line.detail}
        </span>
      )}
    </>
  );

  return (
    <li id={line.anchor} aria-current={destaque ? "true" : undefined} className={linhaClasse(destaque)}>
      <Hora iso={event.at} hoje={hoje} />
      <Glifo status={event.status} glyph={line.glyph} />
      <span className="min-w-0 flex-1 break-words">
        {tecnico ? (
          <button
            type="button"
            aria-expanded={aberto}
            onClick={() => onToggle(!aberto)}
            className="pressable block w-full rounded-sm text-left hover:underline"
          >
            {texto}
          </button>
        ) : (
          <span className="line-clamp-3 sm:line-clamp-none">{texto}</span>
        )}
        {tecnico && aberto && <Tecnico texto={tecnico} />}
      </span>
      {destaque && <span className="eyebrow shrink-0 text-down">do link</span>}
      {!mudanca && event.latency_ms !== undefined && event.latency_ms > 0 && (
        <span className="tabular hidden shrink-0 text-small text-ink-3 sm:inline">
          {latency(event.latency_ms)}
        </span>
      )}
    </li>
  );
}

/**
 * EventGroup é uma sequência de falhas com a mesma causa: uma linha com
 * quantas vezes e entre quando, que abre para as individuais com o texto
 * técnico de cada uma. Abre sozinho quando a falha do link está dentro.
 */
function EventGroup({
  group,
  anchor,
  ehHoje,
  aberto,
  onToggle,
}: {
  group: Extract<TimelineEntry, { kind: "group" }>;
  anchor?: string;
  ehHoje: (iso: string) => boolean;
  aberto: boolean;
  onToggle: (abrir: boolean) => void;
}) {
  const id = `grupo-${group.lines[group.lines.length - 1]!.anchor}`;
  const primeiro = group.lines[0]!;
  const posLink = group.lines.findIndex((l) => l.anchor === anchor);
  const temLink = posLink >= 0;
  const quando = (iso: string) => (ehHoje(iso) ? HORA_SEG.format(new Date(iso)) : stamp(iso));

  // Aberto pelo link, o grupo mostra só a falha e duas vizinhas de cada
  // lado: trinta linhas iguais devolveriam a tela comprida que o
  // agrupamento veio encurtar. Aberto por clique, mostra tudo — aí a
  // pessoa pediu para ver.
  const [inteiro, setInteiro] = useState({ recentes: !temLink, antigas: !temLink });
  const inicio = inteiro.recentes ? 0 : Math.max(0, posLink - VIZINHAS);
  const fim = inteiro.antigas ? group.lines.length : Math.min(group.lines.length, posLink + VIZINHAS + 1);
  const visiveis = group.lines.slice(inicio, fim);
  const restantesAntigas = group.lines.length - fim;

  function clicar() {
    if (!aberto) setInteiro({ recentes: true, antigas: true });
    onToggle(!aberto);
  }

  const mais = (n: number, lado: "recentes" | "antigas") => (
    <li className="border-b border-line py-1.5 pl-[110px] sm:pl-[158px]">
      <button
        type="button"
        onClick={() => setInteiro((v) => ({ ...v, [lado]: true }))}
        className="pressable text-action rounded-sm text-small text-ink-2 underline decoration-line-strong hover:decoration-ink"
      >
        mostrar as outras {n} mais {lado}
      </button>
    </li>
  );

  return (
    <li>
      <div className={linhaClasse(false)}>
        <Hora iso={group.newest} hoje={ehHoje(group.newest)} />
        <Glifo status={primeiro.event.status} glyph={primeiro.glyph} />
        <button
          type="button"
          aria-expanded={aberto}
          aria-controls={id}
          onClick={clicar}
          className="pressable flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2 rounded-sm text-left hover:underline"
        >
          <span className="sr-only">falhas seguidas: </span>
          <span className="text-body text-ink-2">{group.summary}</span>
          <span className="tabular text-small text-ink-3">
            · {group.lines.length} vezes · {quando(group.oldest)}–{quando(group.newest)}
          </span>
          <span aria-hidden className="text-small text-ink-3">
            {aberto ? "▾" : "▸"}
          </span>
        </button>
        {temLink && !aberto && <span className="eyebrow shrink-0 text-down">do link</span>}
      </div>
      {aberto && (
        <ol id={id} className="bg-sunken">
          {inicio > 0 && mais(inicio, "recentes")}
          {visiveis.map((l) => (
            <li
              key={l.anchor + l.event.probe_id}
              id={l.anchor}
              aria-current={l.anchor === anchor ? "true" : undefined}
              className={linhaClasse(l.anchor === anchor)}
            >
              <Hora iso={l.event.at} hoje={ehHoje(l.event.at)} />
              <span aria-hidden className="w-3 shrink-0" />
              <span className="min-w-0 flex-1 text-small text-ink-2 [overflow-wrap:anywhere]">
                {l.cause?.raw || l.label}
              </span>
              {l.anchor === anchor && <span className="eyebrow shrink-0 text-down">do link</span>}
            </li>
          ))}
          {restantesAntigas > 0 && mais(restantesAntigas, "antigas")}
        </ol>
      )}
    </li>
  );
}

/**
 * Incidents lista as quedas confirmadas.
 *
 * Diferente da linha do tempo: aqui só entra o que passou pelo limiar de
 * confirmação e gerou alerta. É o histórico que se leva para uma conversa
 * sobre disponibilidade.
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
            {i.acknowledgement && (
              <span className="shrink-0 text-small text-ink-3" title={i.acknowledgement.note || undefined}>
                visto por {i.acknowledgement.by}
              </span>
            )}
          </span>

          {/* Duração à direita numa coluna própria: durante uma conversa
              sobre disponibilidade é o número que se compara entre linhas. */}
          <span className="flex shrink-0 items-baseline gap-3 text-small">
            <span className="tabular w-[64px] text-right text-ink">
              {duration(i.duration_seconds)}
            </span>
            <span className="tabular hidden text-ink-3 sm:inline">{stamp(i.started_at)}</span>
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * Recipients diz quem é avisado quando este alvo cair, e de onde vem
 * cada canal: do próprio monitor, do ambiente ou do sistema. Sem a
 * origem, um canal herdado parece vínculo esquecido no monitor, e alguém
 * desmarca a caixa errada esperando que ele pare de avisar.
 */
function Recipients({
  monitor,
  grupo,
  sistema,
  administra,
  vincula,
}: {
  monitor: Monitor;
  grupo?: Group;
  sistema?: Group;
  administra: boolean;
  /** Vincula canais: só quem tem escopo total, como no servidor. */
  vincula: boolean;
}) {
  const [linhas, setLinhas] = useState<{ channel: Channel; origins: string[] }[] | null>(null);

  const carregar = useCallback(async () => {
    // Um grupo fora do escopo responde 404; a origem dele some em vez de
    // derrubar a aba inteira.
    const deGrupo = (id: number) =>
      api.groupChannels(id).then(
        (r) => r.items,
        () => [] as Channel[],
      );

    const [direto, doGrupo, doSistema] = await Promise.all([
      api.monitorChannels(monitor.id).then((r) => r.items),
      grupo ? deGrupo(grupo.id) : Promise.resolve([] as Channel[]),
      sistema ? deGrupo(sistema.id) : Promise.resolve([] as Channel[]),
    ]);

    const ambiente = grupo?.parent_id ? "do ambiente" : "do sistema";
    setLinhas(
      channelOrigins([
        { origin: "deste monitor", channels: direto },
        ...(grupo ? [{ origin: `${ambiente} ${crumb(grupo.path)}`, channels: doGrupo }] : []),
        ...(sistema ? [{ origin: `do sistema ${sistema.name}`, channels: doSistema }] : []),
      ]),
    );
    // Pelos ids, e não pelos objetos: o detalhe relê os grupos a cada
    // 30 s, e cada leitura traz objetos novos com o mesmo conteúdo.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [monitor.id, grupo?.id, grupo?.path, sistema?.id, sistema?.name]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  if (linhas === null) return <Loading what="avisos" />;

  return (
    <>
      {linhas.length === 0 ? (
        <Nothing hint="Sem canal, a queda é registrada mas ninguém fica sabendo na hora.">
          Ninguém é avisado quando este alvo cair.
        </Nothing>
      ) : (
        <ul className="border-t border-line">
          {linhas.map(({ channel, origins }) => (
            <li
              key={channel.id}
              className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 border-b border-line py-2"
            >
              <span className="flex min-w-0 items-baseline gap-2">
                <span className="truncate text-body font-medium">{channel.name}</span>
                <span className="eyebrow">{channelTypeLabel(channel.type)}</span>
              </span>
              <span className="text-small text-ink-3">{origins.join(" · ")}</span>
            </li>
          ))}
        </ul>
      )}

      {administra && !vincula && (
        <p className="max-w-prose pt-2 text-small text-ink-3">{NO_CHANNEL_LINK_HINT}</p>
      )}

      {vincula && (
        <div className="flex flex-col gap-2 pt-2">
          <h3 className="eyebrow">canais vinculados a este monitor</h3>
          <MonitorChannels monitorID={monitor.id} onChange={carregar} />
          {grupo && (
            <p className="text-small text-ink-3">
              Os canais do grupo valem para todos os monitores dele e se mudam em{" "}
              <InlineLink to={{ name: "ajustes", section: "grupos" }}>ajustes</InlineLink>.
            </p>
          )}
        </div>
      )}
    </>
  );
}
