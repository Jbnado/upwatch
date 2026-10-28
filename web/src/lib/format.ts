/** Formatação de medidas. Números mal formatados fazem uma interface de
 *  precisão parecer amadora, então cada função aqui tem uma regra só. */

const NUMERO = new Intl.NumberFormat("pt-BR");

/**
 * latency escolhe a unidade pela magnitude.
 *
 * Mostrar "1483 ms" obriga o leitor a contar casas; "1,48 s" é lido de
 * imediato. A troca acontece no segundo, onde a intuição muda.
 */
export function latency(ms: number | null): string {
  if (ms === null || !Number.isFinite(ms) || ms < 0) return "—";
  if (ms === 0) return "0 ms";
  if (ms < 1000) return `${Math.round(ms)} ms`;

  return `${NUMERO.format(Number((ms / 1000).toFixed(2)))} s`;
}

/**
 * uptime usa duas casas.
 *
 * A diferença entre 99,9% e 99,99% é a diferença entre nove horas e
 * cinquenta minutos de indisponibilidade por ano; arredondar para
 * inteiro apagaria justamente o número que se contrata.
 *
 * Aceita null porque ausência de medição precisa atravessar até aqui:
 * "0%" afirma queda total, e afirmar isso sobre o que não se mediu é
 * pior do que não mostrar número.
 */
export function uptime(percent: number | null): string {
  if (percent === null || !Number.isFinite(percent)) return "—";

  const casas = percent === 100 || percent === 0 ? 0 : 2;
  return `${NUMERO.format(Number(percent.toFixed(casas)))}%`;
}

/** duration escreve um intervalo em segundos de forma legível. */
export function duration(seconds: number): string {
  if (seconds < 60) return `${seconds} s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h`;
  return `${Math.round(seconds / 86400)} d`;
}

const HORA = new Intl.DateTimeFormat("pt-BR", { hour: "2-digit", minute: "2-digit" });
const DATA_HORA = new Intl.DateTimeFormat("pt-BR", {
  day: "2-digit",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
});

/** clockTime mostra só a hora, para séries dentro do mesmo dia. */
export function clockTime(iso: string): string {
  return HORA.format(new Date(iso));
}

/** stamp mostra data e hora, para séries que cruzam dias. */
export function stamp(iso: string): string {
  return DATA_HORA.format(new Date(iso));
}

/**
 * ago descreve há quanto tempo algo aconteceu.
 *
 * Durante um incidente, "há 4 min" responde a pergunta que se está
 * fazendo; um horário absoluto obriga a fazer a conta de cabeça.
 */
export function ago(iso: string, now: Date = new Date()): string {
  const segundos = Math.max(0, Math.floor((now.getTime() - new Date(iso).getTime()) / 1000));

  if (segundos < 10) return "agora";
  if (segundos < 60) return `há ${segundos} s`;
  if (segundos < 3600) return `há ${Math.floor(segundos / 60)} min`;
  if (segundos < 86400) return `há ${Math.floor(segundos / 3600)} h`;
  return `há ${Math.floor(segundos / 86400)} d`;
}

/**
 * elapsed diz há quanto tempo um estado dura, sem o "há".
 *
 * Duas unidades a partir da hora: numa queda longa, "2 h" e "2 h 50 min"
 * pedem decisões diferentes. Abaixo da hora, minuto inteiro basta.
 */
export function elapsed(iso: string, now: Date = new Date()): string {
  const s = Math.max(0, Math.floor((now.getTime() - new Date(iso).getTime()) / 1000));

  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min`;
  if (s < 86400) {
    const h = Math.floor(s / 3600);
    const min = Math.floor((s % 3600) / 60);
    return min > 0 ? `${h} h ${min} min` : `${h} h`;
  }
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  return h > 0 ? `${d} d ${h} h` : `${d} d`;
}

/**
 * sinceClock escreve o "desde quando": só a hora se for hoje, com o dia
 * se não for. "Desde 11:31" de ontem seria lido como meia hora de queda.
 */
export function sinceClock(iso: string, now: Date = new Date()): string {
  const quando = new Date(iso);
  const hoje =
    quando.getFullYear() === now.getFullYear() &&
    quando.getMonth() === now.getMonth() &&
    quando.getDate() === now.getDate();

  return hoje ? clockTime(iso) : stamp(iso);
}
