import { describe, expect, it } from "vitest";
import type { GroupSummary } from "../api/types";
import {
  byProblem,
  cardWord,
  crumb,
  hasGroups,
  matches,
  organizeCards,
  problemWord,
  shortName,
} from "./groups";

/**
 * A tela inicial inteira se apoia nestas regras: em que ordem o problema
 * aparece, que palavra o cartão diz e como os ambientes se juntam sob o
 * sistema. Errar aqui não quebra nada à vista — o vermelho só some no
 * meio dos verdes, que é o defeito que a #49 veio corrigir.
 */

function cartao(over: Partial<GroupSummary>): GroupSummary {
  return {
    group_id: 1,
    parent_id: null,
    name: "RPA",
    path: "RPA",
    status: "up",
    total: 0,
    up: 0,
    degraded: 0,
    down: 0,
    unknown: 0,
    paused: 0,
    problems: 0,
    ...over,
  };
}

describe("byProblem", () => {
  const item = (name: string, status: GroupSummary["status"], paused = false) => ({
    name,
    status,
    paused,
  });

  it("põe fora do ar, degradado e sem medição antes do que está no ar", () => {
    // Um alvo vermelho no meio de sessenta verdes some. A ordem é a
    // mesma da API de problemas, para as duas telas não discordarem.
    const got = byProblem(
      [item("a", "up"), item("b", "unknown"), item("c", "down"), item("d", "degraded")],
      (x) => x,
    );

    expect(got.map((x) => x.name)).toEqual(["c", "d", "b", "a"]);
  });

  it("ordena por nome dentro do mesmo estado, com número em ordem natural", () => {
    // Ordem de cadastro faria a lista dançar a cada monitor novo; e
    // "worker 10" antes de "worker 2" parece lista quebrada.
    const got = byProblem(
      [item("worker 10", "up"), item("worker 2", "up"), item("api", "up")],
      (x) => x,
    );

    expect(got.map((x) => x.name)).toEqual(["api", "worker 2", "worker 10"]);
  });

  it("deixa o pausado por último, mesmo que o último estado tenha sido falha", () => {
    // Pausar é decisão de alguém; o estado congelado de antes da pausa
    // não é problema de agora e não pode disputar o topo.
    const got = byProblem([item("parado", "down", true), item("vivo", "up")], (x) => x);

    expect(got.map((x) => x.name)).toEqual(["vivo", "parado"]);
  });

  it("não altera a lista recebida", () => {
    const lista = [item("b", "up"), item("a", "down")];
    byProblem(lista, (x) => x);

    expect(lista.map((x) => x.name)).toEqual(["b", "a"]);
  });
});

describe("cardWord", () => {
  it("diz ok quando nada está errado", () => {
    expect(cardWord(cartao({ total: 7, up: 7 }))).toBe("ok");
  });

  it("conta os problemas quando algo caiu", () => {
    expect(cardWord(cartao({ status: "down", total: 7, up: 6, down: 1, problems: 1 }))).toBe(
      "1 problema",
    );
    expect(
      cardWord(cartao({ status: "down", total: 7, up: 4, down: 2, unknown: 1, problems: 3 })),
    ).toBe("3 problemas");
  });

  it("diz lento quando só há degradação", () => {
    // Lento é outra conversa que fora do ar: o serviço responde. Uma
    // contagem genérica de "problemas" apagaria essa diferença.
    expect(cardWord(cartao({ status: "degraded", total: 7, up: 5, degraded: 2, problems: 2 }))).toBe(
      "lento",
    );
  });

  it("diz sem medição quando só falta medida", () => {
    expect(cardWord(cartao({ status: "unknown", total: 7, up: 6, unknown: 1, problems: 1 }))).toBe(
      "sem medição",
    );
  });

  it("volta a contar quando lento e sem medição se misturam", () => {
    expect(
      cardWord(cartao({ status: "degraded", total: 7, up: 5, degraded: 1, unknown: 1, problems: 2 })),
    ).toBe("2 problemas");
  });

  it("distingue grupo vazio e grupo todo pausado de grupo no ar", () => {
    // "ok" num grupo sem nada medido afirmaria o que ninguém conferiu.
    expect(cardWord(cartao({ total: 0 }))).toBe("vazio");
    expect(cardWord(cartao({ status: "unknown", total: 3, paused: 3 }))).toBe("pausado");
  });
});

describe("organizeCards", () => {
  const rpa = cartao({ group_id: 1, name: "RPA", path: "RPA" });
  const rpaHml = cartao({ group_id: 2, parent_id: 1, name: "hml", path: "RPA / hml" });
  const rpaProd = cartao({ group_id: 3, parent_id: 1, name: "prod", path: "RPA / prod" });
  const ret = cartao({ group_id: 4, name: "Retaguarda", path: "Retaguarda" });
  const retProd = cartao({ group_id: 5, parent_id: 4, name: "prod", path: "Retaguarda / prod" });
  const semGrupo = cartao({ group_id: null, name: "Sem grupo", path: "Sem grupo" });

  it("junta os ambientes sob o sistema, um cartão por ambiente", () => {
    const got = organizeCards([rpa, rpaHml, rpaProd, ret, retProd]);

    expect(got.map((s) => s.title)).toEqual(["RPA", "Retaguarda"]);
    expect(got[0]!.cards.map((c) => c.name)).toEqual(["hml", "prod"]);
    expect(got[1]!.cards.map((c) => c.name)).toEqual(["prod"]);
  });

  it("mostra o próprio sistema como cartão quando ele não tem ambientes", () => {
    // O cartão do sistema soma os ambientes; com eles na tela, repeti-lo
    // contaria cada problema duas vezes. Sem ambientes, é o único cartão.
    const got = organizeCards([ret]);

    expect(got).toHaveLength(1);
    expect(got[0]!.cards.map((c) => c.group_id)).toEqual([4]);
  });

  it("acha o sistema pelo caminho quando o escopo só alcança o ambiente", () => {
    // Quem só tem acesso a "Retaguarda / prod" não recebe o cartão do
    // sistema, mas o ambiente precisa continuar sob o nome certo.
    const got = organizeCards([retProd]);

    expect(got.map((s) => s.title)).toEqual(["Retaguarda"]);
    expect(got[0]!.cards.map((c) => c.group_id)).toEqual([5]);
  });

  it("deixa Sem grupo por último, numa seção sem título", () => {
    const got = organizeCards([semGrupo, rpa, rpaProd]);

    expect(got.map((s) => s.title)).toEqual(["RPA", ""]);
    expect(got[1]!.cards[0]!.group_id).toBeNull();
  });
});

describe("hasGroups", () => {
  it("é falso para instalação que só tem Sem grupo", () => {
    // Instalação antiga, sem grupo nenhum, cai direto na lista: cartão
    // único de "Sem grupo" seria um clique a mais para chegar no mesmo.
    expect(hasGroups([cartao({ group_id: null })])).toBe(false);
    expect(hasGroups([])).toBe(false);
    expect(hasGroups([cartao({ group_id: 1 })])).toBe(true);
  });
});

describe("shortName", () => {
  it("tira o grupo repetido no começo do nome", () => {
    // Antes dos grupos, o nome carregava o ambiente ("rpa-prod bff");
    // dentro do grupo, repetir isso é ruído em toda linha.
    expect(shortName("rpa-prod bff", "RPA / prod")).toBe("bff");
    expect(shortName("RPA prod: worker default", "RPA / prod")).toBe("worker default");
    expect(shortName("prod_bff", "RPA / prod")).toBe("bff");
  });

  it("ignora acento e caixa ao comparar", () => {
    expect(shortName("producao api", "Retaguarda / Produção")).toBe("api");
  });

  it("só tira palavra inteira", () => {
    // "producao" não é o ambiente "prod"; cortar ali mutilaria o nome.
    expect(shortName("producao api", "RPA / prod")).toBe("producao api");
  });

  it("não deixa o nome vazio", () => {
    expect(shortName("rpa prod", "RPA / prod")).toBe("rpa prod");
    expect(shortName("bff", "")).toBe("bff");
  });
});

describe("crumb", () => {
  it("troca a barra do caminho pelo ponto da interface", () => {
    expect(crumb("RPA / prod")).toBe("RPA · prod");
    expect(crumb("")).toBe("Sem grupo");
  });
});

describe("problemWord", () => {
  it("fala de sinal no push e de ar nos demais", () => {
    // Push não tem endereço para "cair": o que falta é o serviço avisar
    // que está vivo, e é isso que quem lê precisa entender.
    expect(problemWord("down", "http")).toBe("fora do ar");
    expect(problemWord("down", "push")).toBe("sem sinal");
    expect(problemWord("unknown", "push")).toBe("aguardando sinal");
    expect(problemWord("degraded", "tcp")).toBe("lento");
    expect(problemWord("unknown", "dns")).toBe("sem medição");
  });
});

describe("matches", () => {
  const bff = { name: "bff", target: "http://bff:3000/health", status: "down" as const, paused: false };

  it("busca pelo nome ou pelo alvo, sem caixa nem acento", () => {
    expect(matches(bff, "BFF", "todos")).toBe(true);
    expect(matches(bff, "3000", "todos")).toBe(true);
    expect(matches({ ...bff, name: "conciliação" }, "conciliacao", "todos")).toBe(true);
    expect(matches(bff, "worker", "todos")).toBe(false);
  });

  it("filtra pelo estado, e pausado é estado à parte", () => {
    // O estado de um pausado é de antes da pausa; contá-lo em "fora do
    // ar" faria o filtro mostrar um problema que ninguém precisa resolver.
    expect(matches(bff, "", "down")).toBe(true);
    expect(matches(bff, "", "up")).toBe(false);
    expect(matches({ ...bff, paused: true }, "", "down")).toBe(false);
    expect(matches({ ...bff, paused: true }, "", "pausado")).toBe(true);
  });
});
