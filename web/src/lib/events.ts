import type { MonitorEvent, Problem, Status } from "../api/types";
import { causeKey, explainCause, type Cause } from "./cause";
import { elapsed } from "./format";
import type { Route } from "./router";

/**
 * Regras da linha do tempo do monitor: como cada evento se lê, como um
 * problema da tela inicial aponta para a falha que o explica e o que
 * dizer quando não há nada a mostrar.
 */

/**
 * eventAnchor é o identificador de um evento no endereço.
 *
 * Instante em milissegundos, que é estável entre a lista de problemas e a
 * linha do tempo — as duas leem a mesma batida. O prefixo separa mudança e
 * falha, que o servidor manda com o mesmo carimbo.
 */
export function eventAnchor(e: Pick<MonitorEvent, "type" | "at">): string {
  return `${e.type === "failure" ? "falha" : "mudanca"}-${Date.parse(e.at)}`;
}

/**
 * problemRoute leva do problema ao detalhe, na aba de eventos, com a
 * falha mais recente em destaque. Só fora do ar e degradado geram falha;
 * sem medição não tem linha para destacar.
 */
export function problemRoute(p: Problem): Route {
  if (p.status === "unknown" || !p.last_check_at) return { name: "monitor", id: p.monitor_id };
  return {
    name: "monitor",
    id: p.monitor_id,
    anchor: eventAnchor({ type: "failure", at: p.last_check_at }),
  };
}

export type TimelineLine = {
  event: MonitorEvent;
  anchor: string;
  /** Decoração; o rótulo diz o mesmo em texto. */
  glyph: string;
  label: string;
  detail?: string;
  /** A causa traduzida e a crua, quando o evento traz mensagem. */
  cause?: Cause;
};

const MUDANCA: Record<Status, { glyph: string; label: string }> = {
  down: { glyph: "▼", label: "caiu" },
  degraded: { glyph: "◆", label: "ficou lento" },
  unknown: { glyph: "○", label: "ficou sem medição" },
  up: { glyph: "▲", label: "voltou" },
};

/**
 * timeline traduz os eventos (do mais recente para o mais antigo) em
 * linhas. A volta diz quanto durou a queda quando o começo dela está na
 * mesma página; sem ele, não se chuta.
 */
export function timeline(events: MonitorEvent[]): TimelineLine[] {
  return events.map((event, i) => {
    const anchor = eventAnchor(event);

    const texto = event.message?.trim();
    const cause = texto ? explainCause(texto) : undefined;

    if (event.type === "failure") {
      return {
        event,
        anchor,
        glyph: "✕",
        label: cause?.summary || (event.status === "degraded" ? "resposta lenta" : "falha sem mensagem"),
        cause,
      };
    }

    const base = MUDANCA[event.status];
    let label = base.label;
    if (event.status === "up") {
      const inicio = events.slice(i + 1).find((e) => e.type === "state");
      if (inicio && inicio.status !== "up") {
        label = `voltou depois de ${elapsed(inicio.at, new Date(event.at))}`;
      }
    }
    return { event, anchor, glyph: base.glyph, label, detail: cause?.summary, cause };
  });
}

export type TimelineEntry =
  | { kind: "line"; line: TimelineLine }
  | {
      kind: "group";
      /** Da mais recente para a mais antiga, como a linha do tempo. */
      lines: TimelineLine[];
      summary: string;
      newest: string;
      oldest: string;
    };

/**
 * groupTimeline junta falhas seguidas com a mesma causa numa entrada só.
 *
 * Dez linhas idênticas de "conexão recusada" empurram para fora da tela a
 * única que diz outra coisa. Só falhas se juntam, e só com o mesmo estado
 * e a mesma causa (sem contar porta efêmera e segundos); mudança de estado
 * nunca entra e sempre quebra a sequência, porque é ela que se procura.
 *
 * Recebe a lista inteira já carregada, de todas as páginas: o grupo que
 * continua na página seguinte sai inteiro, em vez de partido onde a
 * paginação cortou.
 */
export function groupTimeline(lines: TimelineLine[]): TimelineEntry[] {
  const chave = (l: TimelineLine) =>
    l.event.type === "failure" ? `${l.event.status}|${causeKey(l.event.message?.trim() ?? "")}` : null;

  const out: TimelineEntry[] = [];
  let atual: TimelineLine[] = [];

  const fechar = () => {
    if (atual.length === 1) out.push({ kind: "line", line: atual[0]! });
    if (atual.length > 1) {
      out.push({
        kind: "group",
        lines: atual,
        summary: atual[0]!.label,
        newest: atual[0]!.event.at,
        oldest: atual[atual.length - 1]!.event.at,
      });
    }
    atual = [];
  };

  for (const l of lines) {
    const k = chave(l);
    if (k === null) {
      fechar();
      out.push({ kind: "line", line: l });
      continue;
    }
    if (atual.length > 0 && chave(atual[0]!) !== k) fechar();
    atual.push(l);
  }
  fechar();
  return out;
}

/**
 * consecutiveFailures conta as verificações seguidas sem sucesso, do fim
 * para trás. Recebe as batidas em ordem cronológica.
 */
export function consecutiveFailures(beats: { status: Status }[]): number {
  let n = 0;
  for (let i = beats.length - 1; i >= 0; i--) {
    if (beats[i]!.status === "up") break;
    n++;
  }
  return n;
}

/**
 * emptyTimeline é o vazio da aba de eventos, que depende do estado atual.
 *
 * Com o alvo fora do ar, "nada mudou" contradiz o quadro logo acima. O
 * vazio ali quer dizer outra coisa: a falha que derrubou o monitor é mais
 * velha que o histórico guardado.
 */
export function emptyTimeline(
  status: Status,
  filter: "all" | "failure",
): { title: string; hint: string } {
  if (status === "down" || status === "degraded") {
    const estado = status === "down" ? "fora do ar" : "lento";
    return {
      title: "Nenhum evento guardado para este estado.",
      hint: `O monitor está ${estado}, mas as falhas que levaram a isso são anteriores ao histórico guardado (as batidas cruas ficam 7 dias).`,
    };
  }
  if (status === "unknown") {
    return {
      title: "Nenhuma verificação registrada ainda.",
      hint: "O monitor ainda não foi verificado ou, no push, o serviço ainda não mandou sinal.",
    };
  }
  return filter === "failure"
    ? {
        title: "Nenhuma falha registrada.",
        hint: "As batidas com falha ficam guardadas por 7 dias.",
      }
    : {
        title: "Nenhuma falha nem mudança de estado registrada.",
        hint: "As falhas ficam guardadas por 7 dias; as mudanças de estado, sem prazo.",
      };
}
