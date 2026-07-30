import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import { ApiError, type PublicAnnouncement, type PublicDay, type PublicView } from "../api/types";
import { Loading } from "../components/ui";
import { latency, uptime } from "../lib/format";

/**
 * A página pública de estado.
 *
 * Segue a forma que Anthropic, Cloudflare e Google consolidaram, porque
 * quem abre este link já sabe lê-la: um veredito em letra grande no topo,
 * componentes agrupados com o estado à direita, noventa barras por
 * componente, e "incidentes anteriores" embaixo.
 *
 * Duas coisas a diferenciam do painel interno. Não há navegação — quem
 * chega aqui veio de um link e não vai explorar nada. E a linguagem é de
 * quem espera o serviço voltar, não de quem opera: "todos os sistemas
 * operacionais", não "4 alvos, 0 down".
 */

const IMPACTO_CLASSE: Record<string, string> = {
  none: "text-up",
  minor: "text-degraded",
  major: "text-degraded",
  critical: "text-down",
};

const FASE_LABEL: Record<string, string> = {
  investigating: "Investigando",
  identified: "Identificado",
  monitoring: "Monitorando",
  resolved: "Resolvido",
};

const ESTADO_LABEL: Record<string, string> = {
  up: "Operacional",
  degraded: "Desempenho degradado",
  down: "Fora do ar",
  unknown: "Sem medição",
};

const ESTADO_COR: Record<string, string> = {
  up: "text-up",
  degraded: "text-degraded",
  down: "text-down",
  unknown: "text-unknown",
};

const BARRA_COR: Record<string, string> = {
  up: "bg-up",
  degraded: "bg-degraded",
  down: "bg-down",
  unknown: "bg-unknown/40",
};

export function Status({ slug }: { slug?: string }) {
  const [view, setView] = useState<PublicView | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      setView(await api.publicStatus(slug));
      setErro(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        setErro("Esta página de estado não existe.");
        return;
      }
      setErro("Não foi possível carregar o estado agora.");
    }
  }, [slug]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  // Recarrega sozinha: quem deixa esta aba aberta durante uma queda está
  // esperando a notícia de que voltou, e não deveria precisar apertar
  // atualizar para recebê-la.
  useEffect(() => {
    const id = setInterval(() => void carregar(), 60_000);
    return () => clearInterval(id);
  }, [carregar]);

  if (erro) {
    return (
      <main className="mx-auto flex min-h-dvh max-w-3xl items-center justify-center px-5">
        <p className="text-lead text-ink-2">{erro}</p>
      </main>
    );
  }
  if (!view) {
    return (
      <main className="mx-auto max-w-3xl px-5 py-10">
        <Loading what="o estado dos serviços" />
      </main>
    );
  }

  return (
    <main className="mx-auto flex max-w-3xl flex-col gap-10 px-5 py-10">
      <header className="flex flex-col gap-1.5">
        <h1 className="text-title font-semibold tracking-tight">{view.title}</h1>
        {view.description && <p className="text-body text-ink-2">{view.description}</p>}
      </header>

      <Banner view={view} />

      <Ativos announcements={view.announcements} />

      <section className="flex flex-col gap-6">
        {view.groups.map((grupo, i) => (
          <div key={grupo.name || `sem-grupo-${i}`} className="flex flex-col gap-3">
            {grupo.name && <h2 className="text-lead font-medium">{grupo.name}</h2>}
            <div className="flex flex-col">
              {grupo.monitors.map((m) => (
                <Componente
                  key={m.name}
                  name={m.name}
                  status={m.status}
                  uptimePercent={m.uptime_percent}
                  latencyP95={m.latency_p95_ms}
                  history={m.history}
                  windowDays={view.window_days}
                />
              ))}
            </div>
          </div>
        ))}

        {view.groups.length === 0 && (
          <p className="text-body text-ink-2">Nenhum serviço publicado nesta página.</p>
        )}
      </section>

      <Historico announcements={view.announcements} windowDays={view.window_days} />

      <Rodape view={view} />
    </main>
  );
}

/**
 * Banner é a resposta em uma frase.
 *
 * Ocupa a maior tipografia da tela porque é a única coisa que a maioria
 * das visitas vai ler. Quando tudo está bem ele fica sóbrio; a cor entra
 * só quando há o que resolver.
 */
function Banner({ view }: { view: PublicView }) {
  const abertos = view.announcements.filter((a) => a.phase !== "resolved");
  const tudoBem = view.status === "up" && abertos.length === 0;

  // O veredito, não a manchete. O título do incidente já aparece logo
  // abaixo, com a linha do tempo — repeti-lo aqui gastaria a maior
  // tipografia da página numa informação duplicada.
  const texto = veredito(view.status, view.impact, abertos.length > 0);
  const cor = abertos.length > 0 ? IMPACTO_CLASSE[view.impact] : ESTADO_COR[view.status];

  return (
    <section
      className={`flex flex-col gap-1.5 border-l-2 pl-4 ${tudoBem ? "border-up" : "border-current"} ${cor ?? ""}`}
      aria-live="polite"
    >
      <p className="text-hero font-semibold tracking-tight">{texto}</p>
      <p className="text-body text-ink-3">
        Atualizado {carimbo(view.generated_at, view.time_zone)}
      </p>
    </section>
  );
}

/**
 * veredito é a frase do topo.
 *
 * Quando há relato aberto, o impacto declarado por uma pessoa manda: ele
 * sabe de coisas que a sonda não mede — um lote travado, um parceiro fora
 * do ar. Sem relato, sobra o que as verificações mostram.
 */
export function veredito(status: string, impact: string, temRelatoAberto: boolean): string {
  if (temRelatoAberto) {
    switch (impact) {
      case "critical":
        return "Interrupção grave";
      case "major":
        return "Interrupção parcial";
      case "minor":
        return "Desempenho degradado";
      default:
        return "Aviso em andamento";
    }
  }

  switch (status) {
    case "up":
      return "Todos os sistemas operacionais";
    case "degraded":
      return "Desempenho degradado";
    case "down":
      return "Interrupção em andamento";
    default:
      return "Sem medição no momento";
  }
}

/**
 * Ativos destaca o que está em curso.
 *
 * Fica antes dos componentes porque, durante um incidente, o texto que
 * uma pessoa escreveu vale mais do que a cor de qualquer barra.
 */
function Ativos({ announcements }: { announcements: PublicAnnouncement[] }) {
  const abertos = announcements.filter((a) => a.phase !== "resolved");
  if (abertos.length === 0) return null;

  return (
    <section className="flex flex-col gap-4">
      {abertos.map((a) => (
        <Relato key={a.title + a.started_at} a={a} destacado />
      ))}
    </section>
  );
}

function Componente({
  name,
  status,
  uptimePercent,
  latencyP95,
  history,
  windowDays,
}: {
  name: string;
  status: string;
  uptimePercent?: number;
  latencyP95?: number;
  history: PublicDay[];
  windowDays: number;
}) {
  return (
    <div className="flex flex-col gap-2 border-b border-line py-3 last:border-b-0">
      <div className="flex items-baseline justify-between gap-4">
        <span className="text-body font-medium">{name}</span>
        <span className="flex items-baseline gap-3">
          {latencyP95 !== undefined && (
            <span className="tabular text-small text-ink-3">{latency(latencyP95)}</span>
          )}
          <span className={`text-small font-medium ${ESTADO_COR[status] ?? "text-ink-3"}`}>
            {ESTADO_LABEL[status] ?? "Sem medição"}
          </span>
        </span>
      </div>

      <Barras history={history} />

      {/* A legenda existe porque a barra sozinha não diz o período que
          cobre, e "99,98%" sem janela não significa nada. */}
      <div className="flex items-baseline justify-between gap-4 text-micro text-ink-3">
        <span>há {windowDays} dias</span>
        <span className="tabular">
          {uptimePercent === undefined ? "sem medição" : `${uptime(uptimePercent)} de disponibilidade`}
        </span>
        <span>hoje</span>
      </div>
    </div>
  );
}

/**
 * Barras é a faixa de noventa dias.
 *
 * Uma barra por dia, encostadas: é a forma que virou convenção e que
 * qualquer pessoa já sabe ler. Dias sem medição ficam em cinza apagado em
 * vez de vermelho — não houve queda, houve ausência.
 */
function Barras({ history }: { history: PublicDay[] }) {
  return (
    <div className="flex h-8 items-stretch gap-[2px]" role="img" aria-label={resumo(history)}>
      {history.map((d) => (
        <span
          key={d.date}
          title={`${d.date}: ${ESTADO_LABEL[d.status] ?? "Sem medição"}${
            d.uptime_percent === undefined ? "" : ` · ${uptime(d.uptime_percent)}`
          }`}
          className={`flex-1 rounded-xs ${BARRA_COR[d.status] ?? "bg-unknown/40"}`}
        />
      ))}
    </div>
  );
}

function resumo(history: PublicDay[]): string {
  const quedas = history.filter((d) => d.status === "down").length;
  const medidos = history.filter((d) => d.status !== "unknown").length;

  if (medidos === 0) return "Sem medição no período.";
  if (quedas === 0) return `${medidos} dias medidos, nenhum com queda.`;
  return `${medidos} dias medidos, ${quedas} com queda.`;
}

/** Historico é o "incidentes anteriores" das páginas de referência. */
function Historico({
  announcements,
  windowDays,
}: {
  announcements: PublicAnnouncement[];
  windowDays: number;
}) {
  const passados = useMemo(
    () => announcements.filter((a) => a.phase === "resolved"),
    [announcements],
  );

  return (
    <section className="flex flex-col gap-4">
      <h2 className="eyebrow">incidentes anteriores</h2>

      {passados.length === 0 ? (
        // Silêncio é o estado saudável desta seção, e dizê-lo é melhor do
        // que deixar um vazio que parece falha de carregamento.
        <p className="text-body text-ink-2">
          Nenhum incidente relatado nos últimos {windowDays} dias.
        </p>
      ) : (
        <div className="flex flex-col gap-6">
          {passados.map((a) => (
            <Relato key={a.title + a.started_at} a={a} />
          ))}
        </div>
      )}
    </section>
  );
}

/** Relato é um incidente com sua linha do tempo. */
function Relato({ a, destacado = false }: { a: PublicAnnouncement; destacado?: boolean }) {
  const cor = IMPACTO_CLASSE[a.impact] ?? "text-ink";

  return (
    <article
      className={
        destacado
          ? `flex flex-col gap-3 rounded-sm border border-line-strong bg-surface p-4 ${cor}`
          : "flex flex-col gap-3"
      }
    >
      <div className="flex flex-col gap-1.5">
        <h3 className={`text-lead font-medium ${destacado ? cor : "text-ink"}`}>{a.title}</h3>
        <p className="text-small text-ink-3">
          {a.components && a.components.length > 0 && (
            <>
              <span>{a.components.join(", ")}</span>
              <span className="mx-1.5">·</span>
            </>
          )}
          <span className="tabular">{carimboCurto(a.started_at)}</span>
        </p>
      </div>

      {/* A linha do tempo é o motivo de a página existir durante uma
          queda: responde "vocês já sabem, e falta muito?". */}
      {a.updates.length > 0 && (
        <ol className="flex flex-col gap-2.5 border-l border-line pl-4">
          {a.updates.map((u, i) => (
            <li key={u.published_at + i} className="flex flex-col gap-1">
              <span className="flex items-baseline gap-2">
                <span className="text-small font-medium text-ink">
                  {FASE_LABEL[u.phase] ?? "Atualização"}
                </span>
                <span className="tabular text-micro text-ink-3">{carimboCurto(u.published_at)}</span>
              </span>
              <p className="text-body text-ink-2">{u.body}</p>
            </li>
          ))}
        </ol>
      )}
    </article>
  );
}

function Rodape({ view }: { view: PublicView }) {
  return (
    <footer className="flex flex-wrap items-baseline justify-between gap-3 border-t border-line pt-4 text-small text-ink-3">
      {/* Acompanhar sem cadastrar e-mail: é o que a maioria prefere, e o
          que permite ligar num canal de chat sem intermediário. */}
      <a
        href={api.feedUrl(view.slug)}
        className="pressable underline decoration-line-strong hover:decoration-ink active:text-ink-2"
      >
        Acompanhar por Atom
      </a>
      <span>
        {view.time_zone ? `Horários em ${view.time_zone}` : "Horários em UTC"}
        <span className="mx-1.5">·</span>
        UpWatch
      </span>
    </footer>
  );
}

/**
 * carimbo formata no fuso da página.
 *
 * O fuso vem do servidor, e não do navegador, de propósito: quem publica
 * escolhe em que horário a página fala, para que a mesma frase signifique
 * a mesma coisa para todo mundo que a lê.
 */
function carimbo(iso: string, timeZone?: string): string {
  return new Intl.DateTimeFormat("pt-BR", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: timeZone || "UTC",
  }).format(new Date(iso));
}

function carimboCurto(iso: string): string {
  return new Intl.DateTimeFormat("pt-BR", {
    day: "2-digit",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(iso));
}
