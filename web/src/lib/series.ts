import type { Heartbeat, Rollup, Status } from "../api/types";
import type { RangeSource } from "./ranges";

/**
 * Resumo de série temporal.
 *
 * Vive fora das telas porque painel e detalhe precisam chegar ao mesmo
 * número para a mesma janela — em cópias separadas, cada um decidia
 * sozinho o que fazer sem dado, e os dois discordavam.
 *
 * A regra que organiza tudo aqui: ausência de medição é `null`, nunca
 * zero. Zero é uma afirmação — "esteve fora o tempo todo", "respondeu
 * instantaneamente" — e afirmar isso sobre o que não se mediu é a forma
 * mais silenciosa de uma interface mentir.
 */

export type Summary = {
  /** Estado da amostra mais recente. */
  status: Status;
  /** Percentual disponível, ou null se nada foi observado. */
  uptimePercent: number | null;
  p50: number | null;
  p95: number | null;
  p99: number | null;
};

/** summariseHeartbeats resume batidas cruas. */
export function summariseHeartbeats(beats: Heartbeat[]): Summary {
  // "unknown" fica fora do denominador: quando a rede do próprio UpWatch
  // cai, ou quando um monitor push ainda não reportou, a verificação não
  // produziu informação sobre o alvo. Contá-la como queda transformaria
  // falha do observador em falha do observado.
  const observadas = beats.filter((hb) => hb.status !== "unknown");
  const respondidas = beats.filter((hb) => hb.status === "up" || hb.status === "degraded");
  const latencias = respondidas.map((hb) => hb.latency_ms);

  return {
    status: beats.at(-1)?.status ?? "unknown",
    uptimePercent: observadas.length
      ? (observadas.filter((hb) => hb.status !== "down").length / observadas.length) * 100
      : null,
    p50: percentile(latencias, 50),
    p95: percentile(latencias, 95),
    p99: percentile(latencias, 99),
  };
}

/** summariseRollups resume agregados horários ou diários. */
export function summariseRollups(rollups: Rollup[]): Summary {
  const observadas = rollups.reduce((n, r) => n + r.up + r.degraded + r.down, 0);
  const foraDoAr = rollups.reduce((n, r) => n + r.down, 0);
  const ultimo = rollups.at(-1);

  return {
    status: ultimo ? bucketStatus(ultimo.up, ultimo.degraded, ultimo.down) : "unknown",
    uptimePercent: observadas ? ((observadas - foraDoAr) / observadas) * 100 : null,
    p50: pior(rollups.map((r) => r.latency_p50_ms)),
    p95: pior(rollups.map((r) => r.latency_p95_ms)),
    p99: pior(rollups.map((r) => r.latency_p99_ms)),
  };
}

/**
 * bucketStatus resume um período agregado num estado.
 *
 * Qualquer falha no intervalo pesa mais que o resto: uma hora com
 * cinquenta e nove minutos no ar e um fora não é "no ar", e é justamente
 * esse minuto que se procura ao olhar a faixa.
 */
export function bucketStatus(up: number, degraded: number, down: number): Status {
  if (down > 0) return "down";
  if (degraded > 0) return "degraded";
  if (up > 0) return "up";
  return "unknown";
}

/**
 * isStale diz se o monitor deixou de ser verificado.
 *
 * Duas janelas sem notícia já é anomalia: o agendador deveria ter passado
 * e não passou. É falha do UpWatch, não do alvo, e sem o aviso ela se
 * disfarça de "tudo estável" — o pior modo de falhar para uma ferramenta
 * de vigilância.
 *
 * Só responde sobre série crua. Em bucket agregado o carimbo é o início
 * do período, não o instante da verificação: comparar com agora acusava
 * todo monitor de abandonado sempre que alguém escolhia 90 dias, e alarme
 * falso repetido ensina a ignorar o alarme verdadeiro.
 */
export function isStale(
  lastAt: string | undefined,
  intervalSeconds: number,
  source: RangeSource,
  now: Date = new Date(),
): boolean {
  if (source !== "raw" || lastAt === undefined) return false;

  return now.getTime() - new Date(lastAt).getTime() > intervalSeconds * 2000;
}

/**
 * percentile por posto mais próximo, igual ao servidor.
 *
 * Interpolar produziria um número que nenhuma verificação mediu, e o
 * valor divergiria do que a API devolve para a mesma janela — a mesma
 * pergunta daria duas respostas dependendo de quem calculou.
 */
export function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null;

  const ordenados = [...values].sort((a, b) => a - b);
  const posto = Math.ceil((p / 100) * ordenados.length);
  return ordenados[Math.min(Math.max(posto, 1), ordenados.length) - 1]!;
}

/**
 * pior toma o maior percentil da janela.
 *
 * Somar ou tirar média de percentis produziria um número que não
 * corresponde a medição alguma; o pior da janela é uma afirmação
 * verdadeira sobre o período. Bucket sem resposta traz zero, e um
 * conjunto só de zeros significa que ninguém respondeu — ausência.
 */
function pior(values: number[]): number | null {
  const medidos = values.filter((v) => v > 0);
  return medidos.length ? Math.max(...medidos) : null;
}
