/** Formatação de medidas. Números mal formatados fazem uma interface de
 *  precisão parecer amadora, então cada função aqui tem uma regra só. */

const NUMERO = new Intl.NumberFormat("pt-BR");

/**
 * latency escolhe a unidade pela magnitude.
 *
 * Mostrar "1483 ms" obriga o leitor a contar casas; "1,48 s" é lido de
 * imediato. A troca acontece no segundo, onde a intuição muda.
 */
export function latency(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
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
 */
export function uptime(percent: number): string {
  if (!Number.isFinite(percent)) return "—";

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
