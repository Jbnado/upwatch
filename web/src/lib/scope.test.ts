import { describe, expect, it } from "vitest";
import type { Channel, Group } from "../api/types";
import { channelOrigins, covers, scopeLabel, toggleScopeGroup } from "./scope";

/**
 * Acesso por conta e origem dos avisos.
 *
 * O servidor decide quem vê o quê; aqui só se monta a escolha e a frase.
 * Ainda assim a frase precisa ser fiel: um "Retaguarda" exibido para quem
 * na verdade só alcança "Retaguarda · prod" faria alguém conceder menos
 * do que acha que concedeu.
 */

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return {
    id,
    name,
    slug: name.toLowerCase(),
    parent_id,
    path: pai ? `${pai} / ${name}` : name,
    created_at: "",
    updated_at: "",
  };
}

const RPA = grupo(1, "RPA");
const RPA_PROD = grupo(2, "prod", 1, "RPA");
const RPA_HML = grupo(3, "hml", 1, "RPA");
const RET = grupo(4, "Retaguarda");
const RET_PROD = grupo(5, "prod", 4, "Retaguarda");
const GRUPOS = [RPA, RPA_HML, RPA_PROD, RET, RET_PROD];

describe("scopeLabel", () => {
  it("resume o acesso pelo caminho dos grupos", () => {
    expect(scopeLabel({ all_groups: true, group_ids: [] }, GRUPOS)).toBe("Todos os grupos");
    expect(scopeLabel({ all_groups: false, group_ids: [4, 2] }, GRUPOS)).toBe(
      "Retaguarda, RPA · prod",
    );
  });

  it("não cita grupo que deixou de existir", () => {
    // Grupo apagado some da concessão no servidor; mostrá-lo aqui seria
    // prometer um acesso que não existe mais.
    expect(scopeLabel({ all_groups: false, group_ids: [99, 4] }, GRUPOS)).toBe("Retaguarda");
  });
});

describe("toggleScopeGroup", () => {
  it("marca e desmarca", () => {
    expect(toggleScopeGroup([], 2, GRUPOS)).toEqual([2]);
    expect(toggleScopeGroup([2, 5], 2, GRUPOS)).toEqual([5]);
  });

  it("absorve os ambientes quando o sistema é marcado", () => {
    // O sistema já concede os ambientes, inclusive os criados depois.
    // Guardar os dois deixaria um ambiente concedido mesmo depois de o
    // sistema ser desmarcado — mais acesso do que a tela mostra.
    expect(toggleScopeGroup([2, 3, 5], 1, GRUPOS)).toEqual([5, 1]);
  });

  it("não muda nada ao clicar num ambiente já coberto pelo sistema", () => {
    expect(toggleScopeGroup([1], 2, GRUPOS)).toEqual([1]);
  });
});

describe("covers", () => {
  it("vale para o grupo e para os ambientes do sistema marcado", () => {
    expect(covers([1], RPA_PROD)).toBe(true);
    expect(covers([1], RET_PROD)).toBe(false);
    expect(covers([5], RET_PROD)).toBe(true);
  });
});

describe("channelOrigins", () => {
  const canal = (id: number, name: string): Channel => ({
    id,
    name,
    type: "slack",
    enabled: true,
    created_at: "",
    updated_at: "",
  });

  it("diz de onde vem cada canal, sem repetir o canal", () => {
    // O mesmo canal vinculado ao monitor e ao sistema avisa uma vez só
    // (o servidor junta); a tela também precisa mostrar uma linha só.
    const got = channelOrigins([
      { origin: "deste monitor", channels: [canal(1, "plantão")] },
      { origin: "do ambiente RPA · prod", channels: [canal(2, "time rpa")] },
      { origin: "do sistema RPA", channels: [canal(1, "plantão"), canal(3, "diretoria")] },
    ]);

    expect(got).toEqual([
      { channel: canal(1, "plantão"), origins: ["deste monitor", "do sistema RPA"] },
      { channel: canal(2, "time rpa"), origins: ["do ambiente RPA · prod"] },
      { channel: canal(3, "diretoria"), origins: ["do sistema RPA"] },
    ]);
  });
});
