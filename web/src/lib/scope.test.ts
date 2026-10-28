import { describe, expect, it } from "vitest";
import type { Channel, Group } from "../api/types";
import {
  canLinkChannels,
  canRenameGroup,
  channelOrigins,
  covers,
  scopeLabel,
  toggleScopeGroup,
} from "./scope";

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

describe("canRenameGroup", () => {
  // O servidor recusa com 403 renomear grupo cujos irmãos a conta não
  // enxerga inteiros: o conflito de nome com um irmão de fora revelaria
  // que ele existe. Para um sistema, os irmãos são todos os sistemas, e
  // só a conta de escopo total os vê; para um ambiente, basta ter o
  // sistema dele — que é quando ele aparece na lista de grupos da conta.
  const total = { all_groups: true, group_ids: [] };

  it("deixa a conta de escopo total renomear qualquer grupo", () => {
    expect(canRenameGroup(total, RPA, [RPA, RPA_PROD])).toBe(true);
    expect(canRenameGroup(total, RPA_PROD, [RPA, RPA_PROD])).toBe(true);
  });

  it("não deixa a conta restrita renomear sistema, nem o que ela mesma tem", () => {
    const restrita = { all_groups: false, group_ids: [RPA.id] };
    expect(canRenameGroup(restrita, RPA, [RPA, RPA_PROD, RPA_HML])).toBe(false);
  });

  it("deixa renomear o ambiente de um sistema concedido", () => {
    const restrita = { all_groups: false, group_ids: [RPA.id] };
    expect(canRenameGroup(restrita, RPA_PROD, [RPA, RPA_PROD, RPA_HML])).toBe(true);
  });

  it("não deixa renomear o ambiente concedido sozinho, sem o sistema", () => {
    const restrita = { all_groups: false, group_ids: [RET_PROD.id] };
    expect(canRenameGroup(restrita, RET_PROD, [RET_PROD])).toBe(false);
  });
});

describe("canLinkChannels", () => {
  // Canal é recurso de todos os grupos: vincular e desvincular, no
  // monitor ou no grupo, exigem escopo total no servidor.
  it("só vale para quem alcança todos os grupos", () => {
    expect(canLinkChannels({ all_groups: true, group_ids: [] })).toBe(true);
    expect(canLinkChannels({ all_groups: false, group_ids: [1] })).toBe(false);
  });
});
