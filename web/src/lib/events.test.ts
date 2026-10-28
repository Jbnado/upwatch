import { describe, expect, it } from "vitest";
import type { MonitorEvent, Problem } from "../api/types";
import {
  consecutiveFailures,
  emptyTimeline,
  eventAnchor,
  groupTimeline,
  problemRoute,
  timeline,
} from "./events";

/**
 * A linha do tempo é onde se descobre por que o alvo caiu. As regras aqui
 * são as que ligam um problema da tela inicial à falha exata que o
 * explica, e as que impedem a tela de se contradizer.
 */

function problema(over: Partial<Problem>): Problem {
  return {
    monitor_id: 12,
    name: "bff",
    type: "http",
    group_id: 3,
    group_path: "RPA / prod",
    status: "down",
    since: "2026-10-28T11:31:00Z",
    cause: "connection refused",
    last_check_at: "2026-10-28T11:43:00Z",
    ...over,
  };
}

describe("eventAnchor e problemRoute", () => {
  it("aponta o problema para a falha mais recente do monitor", () => {
    // A última verificação de um monitor com problema é a falha que a
    // linha mostra; a âncora dela é a mesma que a aba de eventos desenha.
    const rota = problemRoute(problema({}));
    const falha: MonitorEvent = { type: "failure", at: "2026-10-28T11:43:00Z", status: "down" };

    expect(rota).toEqual({ name: "monitor", id: 12, anchor: eventAnchor(falha) });
  });

  it("separa mudança e falha do mesmo instante", () => {
    // O servidor manda a mudança e a falha que a confirmou com o mesmo
    // carimbo; com a mesma âncora, o destaque cairia na linha errada.
    const at = "2026-10-28T11:31:00Z";
    expect(eventAnchor({ type: "state", at })).not.toBe(eventAnchor({ type: "failure", at }));
  });

  it("não inventa âncora para quem nunca foi medido", () => {
    // Sem medição não há falha para destacar; a âncora apontaria para
    // uma linha que não existe.
    expect(problemRoute(problema({ status: "unknown", last_check_at: null }))).toEqual({
      name: "monitor",
      id: 12,
    });
    expect(problemRoute(problema({ status: "unknown" }))).toEqual({ name: "monitor", id: 12 });
  });

  it("usa só caractere seguro de endereço", () => {
    expect(eventAnchor({ type: "failure", at: "2026-10-28T11:43:00.123456Z" })).toMatch(
      /^[a-z]+-\d+$/,
    );
  });
});

describe("timeline", () => {
  it("diz quanto tempo durou a queda quando o alvo volta", () => {
    const linhas = timeline([
      { type: "state", at: "2026-10-28T09:02:00Z", status: "up", from: "down" },
      { type: "state", at: "2026-10-28T08:58:00Z", status: "down", from: "up", message: "timeout" },
    ]);

    expect(linhas[0]!.label).toBe("voltou depois de 4 min");
    expect(linhas[1]!.label).toBe("caiu");
    expect(linhas[1]!.detail).toBe("timeout");
    // A causa da queda também é traduzida, com o texto cru guardado.
    const traduzida = timeline([
      { type: "state", at: "2026-10-28T08:58:00Z", status: "down", from: "up", message: "dial tcp: i/o timeout" },
    ]);
    expect(traduzida[0]!.detail).toBe("tempo esgotado");
    expect(traduzida[0]!.cause?.raw).toBe("dial tcp: i/o timeout");
  });

  it("não chuta a duração quando o começo da queda não está na página", () => {
    const linhas = timeline([{ type: "state", at: "2026-10-28T09:02:00Z", status: "up", from: "down" }]);

    expect(linhas[0]!.label).toBe("voltou");
  });

  it("mostra a causa de cada falha", () => {
    const linhas = timeline([
      { type: "failure", at: "2026-10-28T11:43:00Z", status: "down", message: "connection refused" },
      { type: "failure", at: "2026-10-28T11:42:00Z", status: "degraded", message: "" },
    ]);

    expect(linhas[0]!.label).toBe("conexão recusada");
    expect(linhas[0]!.cause?.raw).toBe("connection refused");
    expect(linhas[1]!.label).toBe("resposta lenta");
  });

  it("dá a cada linha um texto além do símbolo", () => {
    // O símbolo é decoração: quem não vê cor nem glifo lê o rótulo.
    const linhas = timeline([
      { type: "state", at: "2026-10-28T11:31:00Z", status: "degraded", from: "up" },
      { type: "state", at: "2026-10-28T11:30:00Z", status: "unknown", from: "up" },
    ]);

    expect(linhas.map((l) => l.label)).toEqual(["ficou lento", "ficou sem medição"]);
  });
});

describe("consecutiveFailures", () => {
  it("conta do fim para trás até a última verificação no ar", () => {
    const beats = ["up", "down", "up", "down", "down", "degraded"].map((status) => ({ status }));

    expect(consecutiveFailures(beats as { status: "up" | "down" | "degraded" }[])).toBe(3);
  });

  it("dá zero quando a última verificação foi bem", () => {
    expect(consecutiveFailures([{ status: "down" }, { status: "up" }])).toBe(0);
  });
});

describe("emptyTimeline", () => {
  it("não diz que nada mudou com o monitor fora do ar", () => {
    // Era o defeito: "Nenhuma mudança no período" logo abaixo de "fora do
    // ar". A tela contradizia a si mesma no momento em que mais importava.
    const vazio = emptyTimeline("down", "all");

    expect(vazio.title).not.toMatch(/nenhuma mudança/i);
    expect(vazio.hint).toMatch(/fora do ar/);
  });

  it("explica a ausência sem alarme quando está tudo bem", () => {
    expect(emptyTimeline("up", "all").title).toBe("Nenhuma falha nem mudança de estado registrada.");
    expect(emptyTimeline("up", "failure").title).toBe("Nenhuma falha registrada.");
  });

  it("trata sem medição como ausência de verificação, não como queda", () => {
    expect(emptyTimeline("unknown", "all").title).toBe("Nenhuma verificação registrada ainda.");
  });
});

describe("groupTimeline", () => {
  const falha = (at: string, message: string, status: "down" | "degraded" = "down"): MonitorEvent => ({
    type: "failure",
    at,
    status,
    message,
  });
  const recusada = (porta: number) =>
    `dial tcp 127.0.0.1:${porta}: connectex: Nenhuma conexão pôde ser feita porque a máquina de destino as recusou ativamente.`;

  it("junta falhas seguidas com a mesma causa numa linha só", () => {
    // Dez linhas idênticas de "conexão recusada" escondem a única que
    // diz outra coisa.
    const linhas = timeline(
      Array.from({ length: 9 }, (_, i) =>
        falha(new Date(Date.UTC(2026, 9, 28, 13, 22, 56) - i * 20_000).toISOString(), recusada(1)),
      ),
    );

    const got = groupTimeline(linhas);

    expect(got).toHaveLength(1);
    expect(got[0]!.kind).toBe("group");
    if (got[0]!.kind !== "group") return;
    expect(got[0]!.lines).toHaveLength(9);
    expect(got[0]!.summary).toBe("conexão recusada");
    expect(got[0]!.newest).toBe("2026-10-28T13:22:56.000Z");
    expect(got[0]!.oldest).toBe("2026-10-28T13:20:16.000Z");
  });

  it("nunca põe mudança de estado no grupo, e ela quebra a sequência", () => {
    const got = groupTimeline(
      timeline([
        falha("2026-10-28T13:03:00Z", "connection refused"),
        falha("2026-10-28T13:02:00Z", "connection refused"),
        { type: "state", at: "2026-10-28T13:01:30Z", status: "down", from: "up", message: "connection refused" },
        falha("2026-10-28T13:01:00Z", "connection refused"),
        falha("2026-10-28T13:00:00Z", "connection refused"),
      ]),
    );

    expect(got.map((e) => (e.kind === "group" ? `grupo ${e.lines.length}` : e.line.event.type))).toEqual([
      "grupo 2",
      "state",
      "grupo 2",
    ]);
  });

  it("junta a mesma causa vinda de portas efêmeras diferentes", () => {
    const got = groupTimeline(
      timeline([
        falha("2026-10-28T13:01:00Z", "read tcp 10.0.0.9:51234->10.0.0.5:443: read: connection reset by peer"),
        falha("2026-10-28T13:00:00Z", "read tcp 10.0.0.9:60001->10.0.0.5:443: read: connection reset by peer"),
      ]),
    );

    expect(got).toHaveLength(1);
  });

  it("separa causas e estados diferentes, e deixa a falha solitária sozinha", () => {
    const got = groupTimeline(
      timeline([
        falha("2026-10-28T13:03:00Z", "status 503 fora do esperado"),
        falha("2026-10-28T13:02:00Z", "status 502 fora do esperado"),
        falha("2026-10-28T13:01:00Z", "resposta em 1834ms, acima do limiar de 1s", "degraded"),
        falha("2026-10-28T13:00:00Z", "status 502 fora do esperado"),
      ]),
    );

    expect(got.map((e) => e.kind)).toEqual(["line", "line", "line", "line"]);
  });

  it("une o grupo que continua na página seguinte", () => {
    // A paginação corta por quantidade, não por causa; o grupo precisa
    // sair inteiro depois de "carregar mais antigos".
    const pagina1 = [falha("2026-10-28T13:03:00Z", "connection refused"), falha("2026-10-28T13:02:00Z", "connection refused")];
    const pagina2 = [falha("2026-10-28T13:01:00Z", "connection refused"), falha("2026-10-28T13:00:00Z", "i/o timeout")];

    const got = groupTimeline(timeline([...pagina1, ...pagina2]));

    expect(got[0]!.kind === "group" && got[0]!.lines.length).toBe(3);
    expect(got).toHaveLength(2);
  });
});
