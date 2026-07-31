/**
 * Cálculo das barras da faixa de pulso.
 *
 * Separado do componente de propósito: a regra de altura é a parte do
 * desenho que carrega significado, e como função pura ela é verificável
 * em tabela sem montar DOM.
 */

export type PulseStatus = "up" | "down" | "degraded" | "unknown";

export type PulseSample = {
  /** Instante da verificação, em ISO 8601. */
  at: string;
  status: PulseStatus;
  /** null quando ninguém respondeu: ausência de medição, não zero. */
  latencyMs: number | null;
};

export type PulseBar = PulseSample & {
  /** Altura relativa, de 0 a 1. */
  height: number;
};

/**
 * Altura mínima de uma barra.
 *
 * Sem piso, um serviço que responde instantaneamente viraria uma faixa
 * vazia, e o operador não veria que houve verificação alguma.
 */
export const MIN_BAR = 0.06;

/** Piso das barras que obtiveram resposta, para lerem como textura. */
const MIN_RESPONSIVE_BAR = 0.18;

/** Percentil usado como escala. */
const SCALE_PERCENTILE = 95;

/**
 * Altura em que o p95 é desenhado.
 *
 * Deliberadamente abaixo do teto. Se o p95 ocupasse a altura cheia, um
 * serviço estável — onde o p95 é praticamente o valor típico — viraria
 * uma faixa inteira de barras no talo, lendo como "tudo no limite"
 * quando está tudo calmo. E não sobraria espaço para a degradação
 * aparecer. Com o p95 em dois terços, o comportamento normal é uma banda
 * baixa e serena, e piorar tem para onde subir.
 */
const P95_HEIGHT = 0.65;

/** respondeu informa se a amostra obteve resposta do alvo. */
function respondeu(status: PulseStatus): boolean {
  return status === "up" || status === "degraded";
}

/**
 * percentil por posto mais próximo, sobre valores já ordenados.
 *
 * Mesmo método usado na agregação do servidor: a faixa e o número de p95
 * exibido ao lado precisam concordar, senão o operador vê duas verdades.
 */
function percentil(ordenados: number[], p: number): number {
  if (ordenados.length === 0) return 0;

  const posto = Math.ceil((p / 100) * ordenados.length);
  const indice = Math.min(Math.max(posto, 1), ordenados.length) - 1;
  return ordenados[indice]!;
}

/**
 * pulseBars converte amostras em barras desenháveis.
 *
 * A escala vem do p95 da janela, não do máximo: um único pico de trinta
 * segundos achataria toda a série num traço rente ao chão, escondendo
 * justamente a variação que interessa. Acima do p95 a barra satura.
 */
export function pulseBars(samples: PulseSample[]): PulseBar[] {
  const latencias = samples
    // Latência de amostra sem resposta é ruído: incluí-la na escala
    // distorceria a altura de todas as outras.
    .filter((s) => respondeu(s.status) && s.latencyMs !== null)
    .map((s) => s.latencyMs as number)
    .sort((a, b) => a - b);

  const escala = percentil(latencias, SCALE_PERCENTILE);

  return samples.map((s) => ({ ...s, height: altura(s, escala) }));
}

function altura(s: PulseSample, escala: number): number {
  // Falha é badness máxima, não latência zero. Desenhá-la rente ao chão
  // faria a queda parecer o instante mais saudável da janela.
  if (s.status === "down") return 1;

  // Ausência de medição precisa ser distinta de indisponibilidade: traço
  // fino, não barra cheia.
  if (s.status === "unknown") return MIN_BAR;

  if (escala <= 0 || s.latencyMs === null) return MIN_RESPONSIVE_BAR;

  const proporcao = (s.latencyMs / escala) * P95_HEIGHT;
  return Math.min(Math.max(proporcao, MIN_RESPONSIVE_BAR), 1);
}
