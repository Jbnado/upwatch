import { describe, expect, it } from "vitest";
import { MIN_BAR, pulseBars, type PulseSample } from "./pulse-bars";

/** sample cria uma amostra com valores previsíveis. */
function sample(
  latencyMs: number,
  status: PulseSample["status"] = "up",
  minute = 0,
): PulseSample {
  return {
    at: new Date(Date.UTC(2026, 6, 28, 12, minute)).toISOString(),
    status,
    latencyMs,
  };
}

describe("pulseBars", () => {
  it("produz uma barra por amostra", () => {
    const bars = pulseBars([sample(100), sample(200, "up", 1), sample(300, "up", 2)]);

    expect(bars).toHaveLength(3);
  });

  it("não quebra sem amostra alguma", () => {
    expect(pulseBars([])).toEqual([]);
  });

  // Altura proporcional à latência é o que permite ver o serviço ficando
  // lento antes de cair — a história real de quase todo incidente.
  it("dá barra mais alta para latência maior", () => {
    const bars = pulseBars([sample(50), sample(500, "up", 1)]);

    expect(bars[1]!.height).toBeGreaterThan(bars[0]!.height);
  });

  // Normalizar pelo maior valor faria um único pico achatar toda a série
  // num traço rente ao chão. Normalizar pelo p95 preserva a variação
  // típica e deixa o outlier estourar o teto.
  it("normaliza pelo p95 em vez do máximo", () => {
    const typical = Array.from({ length: 20 }, (_, i) => sample(100, "up", i));
    const withSpike = [...typical, sample(50_000, "up", 20)];

    const semPico = pulseBars(typical);
    const comPico = pulseBars(withSpike);

    // As barras típicas mantêm sua altura mesmo com o pico presente.
    expect(comPico[0]!.height).toBeCloseTo(semPico[0]!.height, 5);
    // E o pico satura em vez de esmagar as demais.
    expect(comPico[20]!.height).toBe(1);
  });

  it("limita a altura ao teto", () => {
    const bars = pulseBars([
      ...Array.from({ length: 20 }, (_, i) => sample(100, "up", i)),
      sample(999_999, "up", 20),
    ]);

    for (const bar of bars) {
      expect(bar.height).toBeLessThanOrEqual(1);
    }
  });

  // Uma falha é badness máxima, não latência zero: desenhá-la rente ao
  // chão faria a queda parecer o instante mais saudável da janela.
  it("desenha falha em altura cheia", () => {
    const bars = pulseBars([sample(100), sample(0, "down", 1)]);

    expect(bars[1]!.height).toBe(1);
  });

  // "Não medimos" precisa ser visualmente distinto de "estava fora": um
  // traço fino em vez de barra cheia.
  it("desenha ausência de medição como traço mínimo", () => {
    const bars = pulseBars([sample(100), sample(0, "unknown", 1)]);

    expect(bars[1]!.height).toBe(MIN_BAR);
    expect(bars[1]!.height).toBeLessThan(bars[0]!.height);
  });

  // Sem piso, um serviço de resposta instantânea viraria uma faixa vazia
  // e o operador não veria que houve verificação.
  it("garante altura mínima para resposta instantânea", () => {
    const bars = pulseBars([sample(0), sample(0, "up", 1)]);

    for (const bar of bars) {
      expect(bar.height).toBeGreaterThanOrEqual(MIN_BAR);
    }
  });

  it("não divide por zero quando toda latência é igual", () => {
    const bars = pulseBars(Array.from({ length: 10 }, (_, i) => sample(120, "up", i)));

    for (const bar of bars) {
      expect(Number.isFinite(bar.height)).toBe(true);
      expect(bar.height).toBeGreaterThan(0);
    }
  });

  // Latência de amostra sem resposta é ruído; incluí-la na escala
  // distorceria a altura de todas as outras.
  it("ignora amostras sem resposta ao calcular a escala", () => {
    const soUp = pulseBars(Array.from({ length: 20 }, (_, i) => sample(100, "up", i)));
    const comQuedas = pulseBars([
      ...Array.from({ length: 20 }, (_, i) => sample(100, "up", i)),
      ...Array.from({ length: 5 }, (_, i) => sample(30_000, "down", 20 + i)),
    ]);

    expect(comQuedas[0]!.height).toBeCloseTo(soUp[0]!.height, 5);
  });

  // Degradado respondeu, só que devagar: é exatamente a amostra que
  // precisa aparecer alta.
  it("mantém amostra degradada na escala de latência", () => {
    const bars = pulseBars([
      ...Array.from({ length: 19 }, (_, i) => sample(100, "up", i)),
      sample(900, "degraded", 19),
    ]);

    expect(bars[19]!.height).toBeGreaterThan(bars[0]!.height);
  });

  // Serviço estável precisa ler como banda baixa e serena, com espaço
  // para a degradação subir. Se o comportamento normal já ocupasse o
  // topo, a faixa diria "tudo no limite" o tempo todo e piorar não teria
  // para onde ir.
  it("desenha serviço estável como banda com folga acima", () => {
    const bars = pulseBars(Array.from({ length: 30 }, (_, i) => sample(120, "up", i)));

    for (const bar of bars) {
      expect(bar.height).toBeLessThan(0.8);
      expect(bar.height).toBeGreaterThan(0.4);
    }
  });

  it("preserva o instante e o estado de cada amostra", () => {
    const entrada = sample(250, "degraded", 7);

    const [bar] = pulseBars([entrada]);

    expect(bar!.at).toBe(entrada.at);
    expect(bar!.status).toBe("degraded");
    expect(bar!.latencyMs).toBe(250);
  });
});
