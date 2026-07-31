import { describe, expect, it } from "vitest";
import { isStale } from "./series";

/**
 * A interface não calcula mais série.
 *
 * Disponibilidade, percentis e a divisão em buckets vêm de
 * `/api/v1/summaries`. O que ficou aqui é a única pergunta que depende do
 * relógio de quem olha, e por isso não faz sentido responder no servidor.
 */

describe("isStale", () => {
  const agora = new Date("2026-07-29T12:00:00Z");
  const atras = (segundos: number) => new Date(agora.getTime() - segundos * 1000).toISOString();

  it("acusa monitor que passou duas janelas sem verificação", () => {
    // O agendador deveria ter passado por aqui e não passou: é falha do
    // UpWatch, não do alvo, e sem este aviso ela se disfarçaria de
    // "tudo estável".
    expect(isStale(atras(150), 60, agora)).toBe(true);
  });

  it("tolera atraso de uma janela", () => {
    // Verificação no limite do intervalo é normal — acusar aqui encheria
    // o painel de alarme falso a cada ciclo.
    expect(isStale(atras(70), 60, agora)).toBe(false);
  });

  it("responde sobre a verificação, não sobre a janela olhada", () => {
    // O instante vem do servidor como fato sobre o monitor. Antes vinha da
    // última amostra desenhada, e isso errava dos dois lados: em bucket
    // agregado o carimbo é o início do período, e na série crua truncada
    // era o fim de uma fatia velha — acusando de abandonado um monitor
    // verificado segundos antes. Escolher 90 dias não muda o fato.
    expect(isStale(atras(10), 60, agora)).toBe(false);
    expect(isStale(atras(86_400), 60, agora)).toBe(true);
  });

  it("não acusa quem ainda não tem amostra alguma", () => {
    // Monitor recém-criado não está atrasado; está começando.
    expect(isStale(undefined, 60, agora)).toBe(false);
    expect(isStale(null, 60, agora)).toBe(false);
  });
});
