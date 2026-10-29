import { describe, expect, it } from "vitest";
import type { Group, Monitor } from "../api/types";
import { alternarAlvo, deVinculos, paraVinculos, resumoDosAlvos } from "./channel-targets";
import { buildServiceTree, filterServiceTree, type ServiceNode } from "./service-tree";

/**
 * O que a árvore do canal grava. Caixa de grupo cheia é vínculo com o
 * grupo, para o monitor criado depois nele já avisar; o resto vai monitor
 * a monitor.
 */

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return { id, name, slug: name, parent_id, path: pai ? `${pai} / ${name}` : name, created_at: "", updated_at: "" };
}

function monitor(id: number, name: string, group_id: number | null): Monitor {
  return {
    id,
    name,
    type: "http",
    target: "",
    interval_seconds: 60,
    timeout_seconds: 5,
    confirmation_threshold: 2,
    degraded_latency_ms: 0,
    group_id,
    enabled: true,
    tags: [],
    created_at: "",
    updated_at: "",
  };
}

const GRUPOS = [grupo(1, "RPA"), grupo(2, "prod", 1, "RPA"), grupo(3, "hml", 1, "RPA"), grupo(4, "Vazio")];
const MONITORES = [monitor(10, "api", 2), monitor(11, "erp", 2), monitor(20, "solto", null)];
const arvore = buildServiceTree(GRUPOS, MONITORES, { emptyGroups: true });

function no(key: string): ServiceNode {
  const achar = (ns: ServiceNode[]): ServiceNode | undefined => {
    for (const n of ns) {
      if (n.key === key) return n;
      const dentro = achar(n.children);
      if (dentro) return dentro;
    }
    return undefined;
  };
  return achar(arvore)!;
}

describe("buildServiceTree com grupos vazios", () => {
  it("mostra o ambiente e o sistema sem monitor", () => {
    expect(arvore.map((n) => n.key)).toEqual(["g1", "g4", "sem-grupo"]);
    expect(no("g1").children.map((n) => n.key)).toEqual(["g3", "g2"]);
  });

  it("acha o grupo vazio pela busca", () => {
    expect(filterServiceTree(arvore, "vazio").map((n) => n.key)).toEqual(["g4"]);
  });

  it("sem a opção, continua escondendo o grupo vazio", () => {
    expect(buildServiceTree(GRUPOS, MONITORES).map((n) => n.key)).toEqual(["g1", "sem-grupo"]);
  });
});

describe("paraVinculos", () => {
  it("sistema inteiro marcado liga o sistema, não os ambientes", () => {
    const sel = alternarAlvo(no("g1"), new Set());
    expect(paraVinculos(arvore, sel)).toEqual({ group_ids: [1], monitor_ids: [] });
  });

  it("ambiente cheio liga o ambiente; serviço sem grupo vai um a um", () => {
    expect(paraVinculos(arvore, new Set(["m10", "m11", "m20"]))).toEqual({
      group_ids: [2],
      monitor_ids: [20],
    });
  });

  it("parte do ambiente vai monitor a monitor", () => {
    expect(paraVinculos(arvore, new Set(["m10", "g4"]))).toEqual({ group_ids: [4], monitor_ids: [10] });
  });
});

describe("deVinculos", () => {
  it("marca tudo dentro do grupo ligado e cada monitor ligado", () => {
    const sel = deVinculos(arvore, { group_ids: [1], monitor_ids: [20] });
    expect([...sel].sort()).toEqual(["g3", "m10", "m11", "m20"]);
    expect(paraVinculos(arvore, sel)).toEqual({ group_ids: [1], monitor_ids: [20] });
  });
});

describe("resumoDosAlvos", () => {
  it("conta grupos e monitores no singular e no plural", () => {
    expect(resumoDosAlvos({ group_ids: [1, 2], monitor_ids: [3] })).toBe("2 grupos, 1 monitor");
    expect(resumoDosAlvos({ group_ids: [1], monitor_ids: [] })).toBe("1 grupo");
    expect(resumoDosAlvos({ group_ids: [], monitor_ids: [] })).toBe("não está ligado a nada");
    expect(resumoDosAlvos(undefined)).toBe("não está ligado a nada");
  });
});
