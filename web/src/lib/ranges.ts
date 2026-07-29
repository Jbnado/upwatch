/**
 * Períodos disponíveis na faixa.
 *
 * Poder olhar sete, trinta ou trezentos e sessenta e cinco dias é o
 * pedido de interface mais votado da categoria. As ferramentas
 * concorrentes não entregam porque guardam apenas dado cru, e uma janela
 * longa viraria uma consulta de milhões de linhas.
 *
 * Aqui a janela escolhe sozinha de qual camada ler. Quem opera pensa em
 * tempo, não em granularidade de armazenamento — a interface nunca
 * pergunta "horário ou diário?".
 */

export type RangeSource = "raw" | "hourly" | "daily";

export type Range = {
  key: string;
  label: string;
  /** Extensão da janela, em horas. */
  hours: number;
  source: RangeSource;
};

export const RANGES: Range[] = [
  { key: "1h", label: "1 h", hours: 1, source: "raw" },
  { key: "6h", label: "6 h", hours: 6, source: "raw" },
  { key: "24h", label: "24 h", hours: 24, source: "raw" },
  { key: "7d", label: "7 d", hours: 24 * 7, source: "hourly" },
  { key: "30d", label: "30 d", hours: 24 * 30, source: "hourly" },
  { key: "90d", label: "90 d", hours: 24 * 90, source: "daily" },
  { key: "365d", label: "1 ano", hours: 24 * 365, source: "daily" },
];

export const DEFAULT_RANGE = RANGES[2]!; // 24 h

export function rangeByKey(key: string): Range {
  return RANGES.find((r) => r.key === key) ?? DEFAULT_RANGE;
}

/** window devolve o intervalo em ISO para a consulta. */
export function windowFor(range: Range, now: Date = new Date()): { from: string; to: string } {
  return {
    from: new Date(now.getTime() - range.hours * 3600_000).toISOString(),
    to: now.toISOString(),
  };
}
