import { describe, expect, it } from "vitest";
import type { Monitor } from "../api/types";
import { agrupar, etiquetasDe } from "./Board";

/**
 * O agrupamento do painel.
 *
 * Existe para separar ambientes — homolog e produção é o caso que o
 * motivou. A regra que mais importa aqui é a de não repetir: um monitor
 * com várias etiquetas aparecendo em dois grupos faria as contagens do
 * topo não fecharem com o que está na tela.
 */

type Leitura = { monitor: Monitor };

function alvo(name: string, ...tags: string[]): Leitura {
  return {
    monitor: {
      id: name.length + tags.length,
      name,
      type: "http",
      target: "https://exemplo.com",
      interval_seconds: 60,
      timeout_seconds: 10,
      confirmation_threshold: 2,
      degraded_latency_ms: 0,
      enabled: true,
      tags,
      created_at: "",
      updated_at: "",
    },
  };
}

describe("etiquetasDe", () => {
  it("reúne sem repetir e em ordem estável", () => {
    // A fileira de fichas precisa aparecer na mesma ordem entre visitas;
    // ordem de cadastro faria ela dançar a cada monitor novo.
    const got = etiquetasDe([
      alvo("a", "producao", "api"),
      alvo("b", "api", "homolog"),
      alvo("c"),
    ]);

    expect(got).toEqual(["api", "homolog", "producao"]);
  });

  it("devolve vazio quando nada foi etiquetado", () => {
    // Sem etiqueta o controle de agrupamento não aparece, senão seria
    // uma escolha entre uma opção só.
    expect(etiquetasDe([alvo("a"), alvo("b")])).toEqual([]);
  });
});

describe("agrupar", () => {
  it("sem etiqueta escolhida, devolve tudo num bloco só", () => {
    const leituras = [alvo("a", "producao"), alvo("b")];

    const got = agrupar(leituras, null);

    expect(got).toHaveLength(1);
    expect(got[0]!.leituras).toHaveLength(2);
  });

  it("separa quem tem a etiqueta de quem não tem", () => {
    const got = agrupar(
      [alvo("prod-1", "producao"), alvo("homolog-1", "homolog"), alvo("prod-2", "producao")],
      "producao",
    );

    expect(got.map((g) => g.nome)).toEqual(["producao", 'sem "producao"']);
    expect(got[0]!.leituras).toHaveLength(2);
    expect(got[1]!.leituras).toHaveLength(1);
  });

  it("não repete um monitor em dois grupos", () => {
    // Um alvo pode ter várias etiquetas, mas cada linha aparece uma vez:
    // repetir faria a soma dos grupos passar do total do topo.
    const leituras = [alvo("ambos", "producao", "homolog")];

    const got = agrupar(leituras, "producao");
    const total = got.reduce((n, g) => n + g.leituras.length, 0);

    expect(total).toBe(leituras.length);
  });

  it("omite o grupo vazio", () => {
    // Se todos têm a etiqueta, não há "sem" a mostrar — um cabeçalho
    // sobre lista vazia faz quem lê procurar o que não está lá.
    const got = agrupar([alvo("a", "producao"), alvo("b", "producao")], "producao");

    expect(got).toHaveLength(1);
    expect(got[0]!.nome).toBe("producao");
  });

  it("mantém quem não tem a etiqueta visível", () => {
    // Sumir com o alvo seria pior que agrupá-lo à parte: ele sumiria sem
    // avisar, e é justamente o que ninguém lembrou de etiquetar que
    // costuma ser esquecido.
    const got = agrupar([alvo("orfao")], "producao");

    expect(got).toHaveLength(1);
    expect(got[0]!.nome).toBe('sem "producao"');
    expect(got[0]!.leituras[0]!.monitor.name).toBe("orfao");
  });

  it("preserva a ordem original dentro de cada grupo", () => {
    const got = agrupar(
      [alvo("primeiro", "p"), alvo("meio"), alvo("ultimo", "p")],
      "p",
    );

    expect(got[0]!.leituras.map((l) => l.monitor.name)).toEqual(["primeiro", "ultimo"]);
  });
});
