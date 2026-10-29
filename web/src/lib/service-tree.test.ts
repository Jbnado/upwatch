import { describe, expect, it } from "vitest";
import type { Group, Monitor } from "../api/types";
import {
  buildServiceTree,
  filterServiceTree,
  leafIds,
  nodeState,
  openByDefault,
  serviceOrigin,
  toggleNode,
  type ServiceNode,
} from "./service-tree";

/**
 * A árvore Sistema › Ambiente › serviço que escolhe o que vai para a
 * página pública e o que um relato afetou.
 *
 * Depois dos grupos, o nome do monitor só é único dentro do ambiente:
 * "banco" existe em quatro lugares. Uma lista plana de nomes obrigava a
 * adivinhar qual dos quatro "banco" ia ser publicado — e publicar o de
 * homologação no lugar do de produção mostra ao cliente um estado que não
 * é o dele.
 */

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return {
    id,
    name,
    slug: name,
    parent_id,
    path: pai ? `${pai} / ${name}` : name,
    created_at: "",
    updated_at: "",
  };
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

const GRUPOS = [
  grupo(1, "RPA"),
  grupo(2, "prod", 1, "RPA"),
  grupo(3, "hml", 1, "RPA"),
  grupo(4, "Retaguarda"),
  grupo(5, "prod", 4, "Retaguarda"),
  grupo(6, "hml", 4, "Retaguarda"),
  grupo(7, "Vazio"),
];

const MONITORES = [
  monitor(10, "api", 2),
  monitor(11, "banco", 2),
  monitor(12, "api", 3),
  monitor(13, "banco", 3),
  monitor(20, "fiscal", 5),
  monitor(21, "erp", 5),
  monitor(22, "banco", 5),
  monitor(23, "erp", 6),
  monitor(30, "legado", null),
];

/** forma desenha a árvore como texto, para comparar a estrutura inteira. */
function forma(nodes: ServiceNode[], nivel = 0): string[] {
  return nodes.flatMap((n) => [
    `${"  ".repeat(nivel)}${n.name}${n.monitorId ? `#${n.monitorId}` : ""}`,
    ...forma(n.children, nivel + 1),
  ]);
}

const arvore = () => buildServiceTree(GRUPOS, MONITORES);

describe("buildServiceTree", () => {
  it("organiza por sistema e ambiente, em ordem de nome, e Sem grupo por último", () => {
    expect(forma(arvore())).toEqual([
      "Retaguarda",
      "  hml",
      "    erp#23",
      "  prod",
      "    banco#22",
      "    erp#21",
      "    fiscal#20",
      "RPA",
      "  hml",
      "    api#12",
      "    banco#13",
      "  prod",
      "    api#10",
      "    banco#11",
      "Sem grupo",
      "  legado#30",
    ]);
  });

  it("deixa de fora o grupo sem monitor", () => {
    // Um sistema vazio no seletor é uma caixa que não publica nada.
    expect(arvore().map((n) => n.name)).not.toContain("Vazio");
  });

  it("guarda em cada serviço o caminho do grupo dele", () => {
    const erp = arvore()[0]!.children[1]!.children[1]!;
    expect(erp.name).toBe("erp");
    expect(erp.path).toBe("Retaguarda · prod");
  });

  it("põe no topo, pelo caminho, o ambiente cujo sistema a conta não vê", () => {
    // Sem o pai na lista, "prod" sozinho não diria de qual sistema é.
    const nos = buildServiceTree([grupo(5, "prod", 4, "Retaguarda")], [monitor(21, "erp", 5)]);
    expect(forma(nos)).toEqual(["Retaguarda · prod", "  erp#21"]);
  });

  it("põe o serviço de um sistema sem ambientes direto sob o sistema", () => {
    const nos = buildServiceTree([grupo(8, "Site")], [monitor(40, "home", 8)]);
    expect(forma(nos)).toEqual(["Site", "  home#40"]);
  });
});

describe("filterServiceTree", () => {
  it("acha pelo nome do serviço, em todos os grupos onde ele existe", () => {
    expect(forma(filterServiceTree(arvore(), "erp"))).toEqual([
      "Retaguarda",
      "  hml",
      "    erp#23",
      "  prod",
      "    erp#21",
    ]);
  });

  it("acha pelo grupo", () => {
    expect(forma(filterServiceTree(arvore(), "retaguarda prod"))).toEqual([
      "Retaguarda",
      "  prod",
      "    banco#22",
      "    erp#21",
      "    fiscal#20",
    ]);
  });

  it("combina nome e grupo", () => {
    expect(forma(filterServiceTree(arvore(), "banco rpa prod"))).toEqual([
      "RPA",
      "  prod",
      "    banco#11",
    ]);
  });

  it("ignora acento e caixa", () => {
    // Quem digita "retaguarda" no teclado de plantão não deveria perder
    // o resultado por causa de um til, nem do lado de lá.
    const nos = buildServiceTree([grupo(9, "Conciliação")], [monitor(50, "Relatório", 9)]);
    expect(forma(filterServiceTree(nos, "CONCILIACAO relatorio"))).toEqual([
      "Conciliação",
      "  Relatório#50",
    ]);
  });

  it("devolve a árvore inteira com a busca vazia", () => {
    expect(filterServiceTree(arvore(), "  ")).toEqual(arvore());
  });
});

describe("seleção", () => {
  const retaguarda = () => arvore()[0]!;
  const retaguardaProd = () => retaguarda().children[1]!;

  it("lista os serviços sob um grupo, em qualquer profundidade", () => {
    expect(leafIds(retaguarda()).sort()).toEqual([20, 21, 22, 23]);
  });

  it("marca o grupo quando todos estão marcados, e parcial quando só alguns", () => {
    // Parcial é o que permite escolher 2 da Retaguarda · prod e ver isso
    // de cara, sem abrir o ambiente.
    expect(nodeState(retaguardaProd(), new Set([21, 20, 22]))).toBe("checked");
    expect(nodeState(retaguardaProd(), new Set([21, 20]))).toBe("mixed");
    expect(nodeState(retaguarda(), new Set([21, 20]))).toBe("mixed");
    expect(nodeState(retaguarda(), new Set())).toBe("unchecked");
  });

  it("marcar o grupo marca os serviços que existem agora", () => {
    const novo = toggleNode(retaguardaProd(), new Set([10]));
    expect([...novo].sort()).toEqual([10, 20, 21, 22]);
  });

  it("marcar um grupo parcial completa em vez de desmarcar", () => {
    // Clicar numa caixa parcial e ver tudo sumir apagaria a escolha feita
    // um a um; completar é o que a caixa promete.
    const novo = toggleNode(retaguardaProd(), new Set([21]));
    expect([...novo].sort()).toEqual([20, 21, 22]);
  });

  it("desmarcar o grupo cheio tira só os serviços dele", () => {
    const novo = toggleNode(retaguardaProd(), new Set([10, 20, 21, 22]));
    expect([...novo]).toEqual([10]);
  });

  it("marca e desmarca um serviço sozinho", () => {
    const erp = retaguardaProd().children[1]!;
    expect([...toggleNode(erp, new Set())]).toEqual([21]);
    expect([...toggleNode(erp, new Set([21]))]).toEqual([]);
  });
});

describe("openByDefault", () => {
  it("abre só os grupos com algo marcado", () => {
    // Fechados, os ambientes dão a visão de conjunto; o que já está
    // publicado não pode ficar escondido atrás de um clique.
    expect([...openByDefault(arvore(), new Set([21]))]).toEqual(["g5"]);
  });
});

describe("serviceOrigin", () => {
  it("diz de qual grupo vem o serviço publicado", () => {
    expect(serviceOrigin(MONITORES[5]!, GRUPOS)).toBe("Retaguarda · prod");
    expect(serviceOrigin(MONITORES[8]!, GRUPOS)).toBe("Sem grupo");
  });
});

describe("dependentes na árvore", () => {
  // A sonda da VM de RPA/prod e as verificações que ela faz. O redis de
  // Retaguarda depende da mesma sonda, mas mora noutro grupo.
  const sonda = { ...monitor(40, "sonda vm-01", 2), type: "push" as const };
  const fila = { ...monitor(41, "fila", 2), parent_id: 40 };
  const worker = { ...monitor(42, "worker", 2), parent_id: 41 };
  const redis = { ...monitor(43, "redis", 5), parent_id: 40 };
  const MAIS = [...MONITORES, sonda, fila, worker, redis];
  const comSonda = () => buildServiceTree(GRUPOS, MAIS);
  const rpaProd = () => comSonda().find((n) => n.name === "RPA")!.children[1]!;

  it("põe o dependente sob o pai do mesmo grupo, em qualquer profundidade", () => {
    expect(forma(rpaProd().children)).toEqual([
      "api#10",
      "banco#11",
      "sonda vm-01#40",
      "  fila#41",
      "    worker#42",
    ]);
  });

  it("deixa no próprio grupo o dependente de um pai de outro grupo", () => {
    // Marcar "Retaguarda › prod" tem de levar o redis, que está lá.
    const retaguardaProd = comSonda()[0]!.children[1]!;
    expect(leafIds(retaguardaProd).sort()).toEqual([20, 21, 22, 43]);
  });

  it("marcar o ambiente marca também os dependentes aninhados", () => {
    expect([...toggleNode(rpaProd(), new Set())].sort()).toEqual([10, 11, 40, 41, 42]);
  });

  it("marcar o pai marca só ele", () => {
    // Avisar sobre a sonda não é o mesmo que avisar sobre o que ela vigia.
    const no = rpaProd().children[2]!;
    expect([...toggleNode(no, new Set())]).toEqual([40]);
    expect(nodeState(no, new Set([41, 42]))).toBe("unchecked");
  });

  it("o travado não entra no grupo, nem na marca nem no estado", () => {
    const travados = new Set([40, 41]);
    expect([...toggleNode(rpaProd(), new Set(), travados)].sort()).toEqual([10, 11, 42]);
    expect(nodeState(rpaProd(), new Set([10, 11, 42]), travados)).toBe("checked");
  });

  it("na busca, o dependente achado aparece sob o pai", () => {
    expect(forma(filterServiceTree(comSonda(), "worker"))).toEqual([
      "RPA",
      "  prod",
      "    sonda vm-01#40",
      "      fila#41",
      "        worker#42",
    ]);
  });

  it("abre o pai com dependente marcado", () => {
    expect([...openByDefault(comSonda(), new Set([42]))].sort()).toEqual(["g2", "m40", "m41"]);
  });
});
