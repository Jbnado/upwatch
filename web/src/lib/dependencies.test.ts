import { describe, expect, it } from "vitest";
import type { Group, Monitor } from "../api/types";
import { aninharDependentes, filhosDe, opcoesDePai, rotuloCalado, travasDeFilho } from "./dependencies";

/**
 * Monitores dependentes: a sonda (pai) e as verificações dela (filhos).
 * Com o pai fora, o filho não avisa, e a tela precisa dizer por quê.
 */

function monitor(id: number, over: Partial<Monitor> = {}): Monitor {
  return {
    id,
    name: `m${id}`,
    type: "http",
    target: "",
    interval_seconds: 60,
    timeout_seconds: 10,
    confirmation_threshold: 3,
    degraded_latency_ms: 0,
    enabled: true,
    tags: [],
    created_at: "",
    updated_at: "",
    ...over,
  };
}

function grupo(id: number, path: string): Group {
  return { id, name: path, slug: path, parent_id: null, path, created_at: "", updated_at: "" };
}

describe("rotuloCalado", () => {
  it("nomeia o pai que cala o monitor", () => {
    expect(rotuloCalado({ id: 3, name: "sonda vm-01" })).toBe("sem sinal — sonda vm-01 fora");
  });

  it("não inventa nome para o pai que a conta não enxerga", () => {
    // O servidor esconde o pai de fora do escopo; o filho ainda precisa
    // dizer por que está calado.
    expect(rotuloCalado({ id: null })).toBe("sem sinal — dependência fora");
  });
});

describe("opcoesDePai", () => {
  const grupos = [grupo(1, "Sondas"), grupo(2, "RPA / prod")];

  it("sugere primeiro o sinal de vida das sondas", () => {
    // O caso comum é ligar a verificação ao sinal de vida da sonda que a
    // faz, e procurá-lo no meio de todos os monitores é o passo que se
    // erra.
    const vida = monitor(1, { type: "push", group_id: 1, name: "sonda vm-01" });
    const api = monitor(2, { group_id: 2, name: "api" });
    const { sugeridos, outros } = opcoesDePai([api, vida], grupos);

    expect(sugeridos.map((m) => m.id)).toEqual([1]);
    expect(outros.map((m) => m.id)).toEqual([2]);
  });

  it("não oferece o próprio monitor nem quem depende dele", () => {
    // Escolher um descendente fecharia um ciclo, que o servidor recusa;
    // não oferecer é melhor que deixar escolher e recusar.
    const eu = monitor(1);
    const filho = monitor(2, { parent_id: 1 });
    const neto = monitor(3, { parent_id: 2 });
    const outro = monitor(4);
    const { sugeridos, outros } = opcoesDePai([eu, filho, neto, outro], grupos, 1);

    expect([...sugeridos, ...outros].map((m) => m.id)).toEqual([4]);
  });
});

describe("filhos", () => {
  const pai = monitor(1);
  const avo = monitor(0);
  const filho = monitor(2, { parent_id: 1 });
  const solto = monitor(3);
  pai.parent_id = 0;

  it("lista os filhos diretos do pai", () => {
    expect(filhosDe(1, [avo, pai, filho, solto]).map((m) => m.id)).toEqual([2]);
  });

  it("trava quem fecharia ciclo e quem já está ligado, dizendo por quê", () => {
    const travas = travasDeFilho(1, [avo, pai, filho, solto]);

    expect(Object.fromEntries(travas)).toEqual({
      0: { checked: false, reason: "este depende dele" },
      1: { checked: false, reason: "é este monitor" },
      2: { checked: true, reason: "já depende deste" },
    });
  });
});

describe("aninharDependentes", () => {
  const ids = (ms: Monitor[]) => ms.map((m) => m.id);

  it("põe o filho sob o pai, mantendo a ordem de entrada", () => {
    const lista = [monitor(3, { parent_id: 1 }), monitor(1), monitor(2, { parent_id: 1 })];
    const { raiz, filhos } = aninharDependentes(lista, (m) => m);

    expect(ids(raiz)).toEqual([1]);
    expect(ids(filhos.get(1)!)).toEqual([3, 2]);
  });

  it("deixa na raiz quem tem o pai fora da lista", () => {
    // O pai noutro grupo, ou escondido pela busca, não leva o filho junto.
    const { raiz } = aninharDependentes([monitor(2, { parent_id: 99 })], (m) => m);
    expect(ids(raiz)).toEqual([2]);
  });

  it("deixa um ciclo antigo na raiz em vez de sumir com ele", () => {
    const lista = [monitor(1, { parent_id: 2 }), monitor(2, { parent_id: 1 }), monitor(3, { parent_id: 2 })];
    const { raiz, filhos } = aninharDependentes(lista, (m) => m);

    expect(ids(raiz)).toEqual([1, 2]);
    expect(ids(filhos.get(2)!)).toEqual([3]);
  });
});
