import { describe, expect, it } from "vitest";
import type { Heartbeat, Rollup } from "../api/types";
import {
  bucketStatus,
  isStale,
  percentile,
  summariseHeartbeats,
  summariseRollups,
} from "./series";

/**
 * Resumo de série.
 *
 * A mesma conta alimentava o painel e o detalhe em cópias separadas, e
 * cada cópia decidia sozinha o que fazer sem dado. Aqui ela é uma só, e
 * a regra que interessa está fixada por teste: ausência de medição é
 * null, nunca zero.
 */

function hb(status: Heartbeat["status"], latency_ms: number, minuto = 0): Heartbeat {
  return {
    timestamp: new Date(Date.UTC(2026, 6, 29, 12, minuto)).toISOString(),
    status,
    latency_ms,
    probe_id: "local",
  };
}

function rollup(campos: Partial<Rollup>): Rollup {
  return {
    bucket_start: new Date(Date.UTC(2026, 6, 29, 12)).toISOString(),
    resolution: "hourly",
    total: 0,
    up: 0,
    down: 0,
    degraded: 0,
    unknown: 0,
    uptime_percent: 0,
    latency_avg_ms: 0,
    latency_min_ms: 0,
    latency_max_ms: 0,
    latency_p50_ms: 0,
    latency_p95_ms: 0,
    latency_p99_ms: 0,
    ...campos,
  };
}

describe("summariseHeartbeats", () => {
  it("não afirma zero por cento quando não houve medição", () => {
    // Monitor push que ainda não recebeu sinal, ou janela anterior à
    // criação do alvo. "0%" ali é a leitura mais alarmante que existe, e
    // é falsa: nada foi observado.
    const resumo = summariseHeartbeats([]);

    expect(resumo.uptimePercent).toBeNull();
    expect(resumo.p95).toBeNull();
    expect(resumo.status).toBe("unknown");
  });

  it("não afirma zero por cento quando só houve amostra sem medição", () => {
    const resumo = summariseHeartbeats([hb("unknown", 0), hb("unknown", 0, 1)]);

    expect(resumo.uptimePercent).toBeNull();
  });

  it("exclui as amostras sem medição do denominador", () => {
    // Se a rede do próprio UpWatch cai, as verificações viram "unknown".
    // Contá-las como queda transformaria falha do observador em falha do
    // observado, e corromperia o número que se leva a uma conversa de SLA.
    const resumo = summariseHeartbeats([
      hb("up", 100),
      hb("unknown", 0, 1),
      hb("unknown", 0, 2),
      hb("down", 0, 3),
    ]);

    expect(resumo.uptimePercent).toBe(50);
  });

  it("conta amostra degradada como disponível", () => {
    // Lento é ruim, mas está no ar: quem responde em dois segundos não
    // esteve fora.
    const resumo = summariseHeartbeats([hb("up", 80), hb("degraded", 900, 1)]);

    expect(resumo.uptimePercent).toBe(100);
  });

  it("mede latência só de quem respondeu", () => {
    // A latência de uma verificação que estourou o tempo é o tempo limite,
    // não a resposta do serviço — incluí-la inventaria um percentil.
    const resumo = summariseHeartbeats([hb("up", 100), hb("down", 5000, 1), hb("up", 200, 2)]);

    expect(resumo.p95).toBe(200);
    expect(resumo.p50).toBe(100);
  });

  it("toma o estado da amostra mais recente", () => {
    const resumo = summariseHeartbeats([hb("up", 100), hb("down", 0, 1)]);

    expect(resumo.status).toBe("down");
  });

  it("devolve zero por cento real quando tudo caiu", () => {
    // Aqui o zero é medição, e precisa aparecer: apagá-lo esconderia uma
    // queda total.
    const resumo = summariseHeartbeats([hb("down", 0), hb("down", 0, 1)]);

    expect(resumo.uptimePercent).toBe(0);
  });
});

describe("summariseRollups", () => {
  it("não afirma zero por cento sem bucket algum", () => {
    const resumo = summariseRollups([]);

    expect(resumo.uptimePercent).toBeNull();
    expect(resumo.p99).toBeNull();
  });

  it("não afirma zero por cento quando os buckets só têm sem-medição", () => {
    const resumo = summariseRollups([rollup({ unknown: 60, total: 60 })]);

    expect(resumo.uptimePercent).toBeNull();
  });

  it("soma observações entre buckets", () => {
    const resumo = summariseRollups([
      rollup({ up: 30, down: 30, total: 60 }),
      rollup({ up: 60, total: 60 }),
    ]);

    expect(resumo.uptimePercent).toBe(75);
  });

  it("toma o pior percentil da janela, não a média deles", () => {
    // Média de percentis não corresponde a medição alguma. O pior da
    // janela é uma afirmação verdadeira sobre o período.
    const resumo = summariseRollups([
      rollup({ up: 60, total: 60, latency_p95_ms: 120 }),
      rollup({ up: 60, total: 60, latency_p95_ms: 480 }),
    ]);

    expect(resumo.p95).toBe(480);
  });

  it("ignora percentil zerado de bucket sem resposta", () => {
    // Bucket em que o alvo só caiu traz percentil zero. Deixá-lo entrar no
    // máximo é inofensivo, mas se for o único bucket o resultado precisa
    // ser ausência, não "0 ms".
    const resumo = summariseRollups([rollup({ down: 60, total: 60 })]);

    expect(resumo.uptimePercent).toBe(0);
    expect(resumo.p95).toBeNull();
  });
});

describe("bucketStatus", () => {
  it("deixa qualquer falha pesar mais que a maioria saudável", () => {
    // Uma hora com 59 minutos no ar e 1 fora não é "no ar": o que se
    // procura numa faixa é justamente o minuto que falhou.
    expect(bucketStatus(59, 0, 1)).toBe("down");
    expect(bucketStatus(59, 1, 0)).toBe("degraded");
    expect(bucketStatus(60, 0, 0)).toBe("up");
    expect(bucketStatus(0, 0, 0)).toBe("unknown");
  });
});

describe("isStale", () => {
  const agora = new Date("2026-07-29T12:00:00Z");
  const atras = (segundos: number) =>
    new Date(agora.getTime() - segundos * 1000).toISOString();

  it("acusa monitor que passou duas janelas sem verificação", () => {
    // O agendador deveria ter passado por aqui e não passou: é falha do
    // UpWatch, não do alvo, e sem este aviso ela se disfarçaria de
    // "tudo estável".
    expect(isStale(atras(150), 60, "raw", agora)).toBe(true);
  });

  it("tolera atraso de uma janela", () => {
    // Verificação no limite do intervalo é normal — acusar aqui encheria
    // o painel de alarme falso a cada ciclo.
    expect(isStale(atras(70), 60, "raw", agora)).toBe(false);
  });

  it("cala a boca quando a série é agregada", () => {
    // Bucket diário carrega o início do dia, não o instante da última
    // verificação. Comparar com "agora" acusava todos os monitores de
    // abandonados sempre que alguém escolhia 90 dias — alarme falso que
    // ensina a ignorar o aviso justo.
    expect(isStale(atras(86_400), 60, "daily", agora)).toBe(false);
    expect(isStale(atras(3_600), 60, "hourly", agora)).toBe(false);
  });

  it("não acusa quem ainda não tem amostra alguma", () => {
    // Monitor recém-criado não está atrasado; está começando.
    expect(isStale(undefined, 60, "raw", agora)).toBe(false);
  });
});

describe("percentile", () => {
  it("usa posto mais próximo, igual ao servidor", () => {
    // Interpolar produziria um número que nenhuma verificação mediu, e o
    // valor divergiria do que a API devolve para a mesma janela.
    expect(percentile([10, 20, 30, 40], 50)).toBe(20);
    expect(percentile([10, 20, 30, 40], 95)).toBe(40);
    expect(percentile([10], 50)).toBe(10);
  });

  it("devolve ausência para série vazia", () => {
    expect(percentile([], 95)).toBeNull();
  });

  it("não depende da ordem de entrada", () => {
    expect(percentile([40, 10, 30, 20], 50)).toBe(20);
  });
});
