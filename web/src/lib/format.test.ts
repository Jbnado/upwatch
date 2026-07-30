import { describe, expect, it } from "vitest";
import { ago, duration, latency, uptime } from "./format";

/**
 * Formatação é onde a interface mente com mais facilidade.
 *
 * Um zero exibido no lugar de "não medi" não parece defeito — parece
 * dado. Estes testes existem sobretudo para prender a diferença entre
 * ausência e valor.
 */

describe("uptime", () => {
  it("distingue ausência de medição de zero por cento", () => {
    // Monitor push recém-criado, ou janela sem verificação alguma: não há
    // percentual a informar. "0%" ali afirmaria indisponibilidade total,
    // que é a leitura mais alarmante possível — e falsa.
    expect(uptime(null)).toBe("—");
    expect(uptime(0)).toBe("0%");
  });

  it("mantém duas casas onde a diferença importa", () => {
    // 99,9% e 99,99% separam nove horas de cinquenta minutos de queda por
    // ano; arredondar apagaria o número que se contrata.
    expect(uptime(99.9)).toBe("99,9%");
    expect(uptime(99.99)).toBe("99,99%");
  });

  it("não enfeita os extremos com casas decimais", () => {
    expect(uptime(100)).toBe("100%");
  });

  it("devolve travessão para número inválido", () => {
    expect(uptime(NaN)).toBe("—");
  });
});

describe("latency", () => {
  it("troca de unidade no segundo", () => {
    expect(latency(999)).toBe("999 ms");
    expect(latency(1000)).toBe("1 s");
    expect(latency(1483)).toBe("1,48 s");
  });

  it("preserva o zero medido", () => {
    // Zero aqui é medição real de cache local, não ausência.
    expect(latency(0)).toBe("0 ms");
  });

  it("recusa valor impossível", () => {
    expect(latency(-1)).toBe("—");
    expect(latency(Infinity)).toBe("—");
  });
});

describe("duration", () => {
  it("sobe de unidade conforme a escala", () => {
    expect(duration(30)).toBe("30 s");
    expect(duration(90)).toBe("2 min");
    expect(duration(7200)).toBe("2 h");
    expect(duration(172800)).toBe("2 d");
  });
});

describe("ago", () => {
  const agora = new Date("2026-07-29T12:00:00Z");
  const atras = (segundos: number) =>
    new Date(agora.getTime() - segundos * 1000).toISOString();

  it("responde em unidades que se lê durante um incidente", () => {
    expect(ago(atras(3), agora)).toBe("agora");
    expect(ago(atras(45), agora)).toBe("há 45 s");
    expect(ago(atras(240), agora)).toBe("há 4 min");
    expect(ago(atras(7200), agora)).toBe("há 2 h");
  });

  it("não inventa futuro quando o relógio do servidor está adiantado", () => {
    // Diferença de relógio entre servidor e navegador produziria "há -3 s".
    expect(ago(new Date(agora.getTime() + 5000).toISOString(), agora)).toBe("agora");
  });
});
