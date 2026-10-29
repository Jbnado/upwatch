import type { Group, MaintenanceWindow, Monitor, Recurrence } from "../api/types";

/**
 * O que a tela de manutenção calcula para escrever cada janela.
 *
 * A regra de quando a janela acontece (recorrência, fuso, encerramento)
 * fica no servidor, que devolve o estado e a próxima ocorrência prontos.
 * Aqui sobra só como dizer isso em português.
 */

export const RECORRENCIA_LABEL: Record<Recurrence, string> = {
  none: "uma vez",
  daily: "todo dia",
  weekly: "toda semana",
};

export const ESTADO_LABEL: Record<MaintenanceWindow["state"], string> = {
  active: "em curso",
  scheduled: "agendada",
  finished: "encerrada",
};

const ORDEM: Record<MaintenanceWindow["state"], number> = { active: 0, scheduled: 1, finished: 2 };

/**
 * ordenar põe a em curso primeiro, depois a próxima a começar, e as
 * encerradas por último, da mais recente para a mais antiga.
 */
export function ordenar(janelas: MaintenanceWindow[]): MaintenanceWindow[] {
  return [...janelas].sort((a, b) => {
    if (ORDEM[a.state] !== ORDEM[b.state]) return ORDEM[a.state] - ORDEM[b.state];
    if (a.state === "finished") return b.starts_at.localeCompare(a.starts_at);
    return (a.next?.start ?? a.starts_at).localeCompare(b.next?.start ?? b.starts_at);
  });
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/**
 * paraCampo converte o instante no valor de um input datetime-local, no
 * fuso do navegador — que é o fuso em que a janela é gravada.
 */
export function paraCampo(iso: string | Date): string {
  const d = typeof iso === "string" ? new Date(iso) : iso;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** doCampo é o inverso: o horário de parede local vira instante ISO. */
export function doCampo(valor: string): string {
  return new Date(valor).toISOString();
}

/** fusoDoNavegador é o fuso em que a recorrência repete o horário. */
export function fusoDoNavegador(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

/**
 * padraoAgora é a janela que o formulário propõe: começa agora e dura
 * meia hora. É o caso que motivou a tela — o deploy que vai sair já.
 */
export function padraoAgora(agora: Date = new Date()): { inicio: string; fim: string } {
  const inicio = new Date(agora);
  inicio.setSeconds(0, 0);
  const fim = new Date(inicio.getTime() + 30 * 60_000);
  return { inicio: paraCampo(inicio), fim: paraCampo(fim) };
}

function hora(d: Date): string {
  return new Intl.DateTimeFormat("pt-BR", { hour: "2-digit", minute: "2-digit" }).format(d);
}

function dia(d: Date): string {
  return new Intl.DateTimeFormat("pt-BR", { weekday: "short", day: "2-digit", month: "short" }).format(d);
}

/**
 * quando descreve a ocorrência que interessa: a em curso ou a próxima;
 * na encerrada, a primeira. "qui., 29 de out. 19:00–19:30", com o fim
 * levando o dia só quando muda.
 */
export function quando(j: MaintenanceWindow): string {
  const inicio = new Date(j.next?.start ?? j.starts_at);
  const fim = new Date(j.next?.end ?? j.ended_at ?? j.ends_at);
  const mesmoDia = inicio.toDateString() === fim.toDateString();
  const intervalo = `${dia(inicio)} ${hora(inicio)}–${mesmoDia ? "" : dia(fim) + " "}${hora(fim)}`;
  if (j.recurrence === "none") return intervalo;
  return `${RECORRENCIA_LABEL[j.recurrence]} · ${j.state === "finished" ? "desde" : "próxima"} ${intervalo}`;
}

/**
 * alvos escreve o alcance: os grupos pelo caminho, os monitores pelo nome
 * com o grupo junto — "erp" existe em prod e em hml. O id que a conta não
 * conhece (apagado depois) fica de fora em vez de aparecer como número.
 */
export function alvos(j: MaintenanceWindow, grupos: Group[], monitores: Monitor[]): string[] {
  const porGrupo = new Map(grupos.map((g) => [g.id, g]));
  const porMonitor = new Map(monitores.map((m) => [m.id, m]));

  const out: string[] = [];
  for (const id of j.group_ids) {
    const g = porGrupo.get(id);
    if (g) out.push(g.parent_id === null ? `${g.path} (todos os ambientes)` : g.path);
  }
  for (const id of j.monitor_ids) {
    const m = porMonitor.get(id);
    if (!m) continue;
    const g = m.group_id != null ? porGrupo.get(m.group_id) : undefined;
    out.push(g ? `${m.name} · ${g.path}` : m.name);
  }
  return out;
}
