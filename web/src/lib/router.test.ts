import { describe, expect, it } from "vitest";
import { href, parseRoute } from "./router";

/**
 * O endereço é o que se cola no chat durante um incidente. Por isso aba e
 * falha em destaque moram na URL: quem abre o link precisa cair no mesmo
 * lugar de quem mandou.
 */

describe("rotas de grupo", () => {
  it("vai e volta pelo endereço", () => {
    expect(parseRoute("/groups/7")).toEqual({ name: "group", id: 7 });
    expect(href({ name: "group", id: 7 })).toBe("/groups/7");
  });

  it("dá endereço próprio a Sem grupo", () => {
    expect(href({ name: "group", id: null })).toBe("/groups/sem-grupo");
    expect(parseRoute("/groups/sem-grupo")).toEqual({ name: "group", id: null });
  });

  it("manda id inválido para o painel em vez de abrir grupo inexistente", () => {
    expect(parseRoute("/groups/abc")).toEqual({ name: "board" });
  });
});

describe("rotas do monitor", () => {
  it("abre em Eventos sem segmento extra", () => {
    // Eventos é a aba padrão; um segmento a mais no endereço mais comum
    // só alongaria o link.
    expect(href({ name: "monitor", id: 3 })).toBe("/monitors/3");
    expect(parseRoute("/monitors/3")).toEqual({ name: "monitor", id: 3 });
  });

  it("guarda as outras abas no caminho", () => {
    expect(href({ name: "monitor", id: 3, tab: "quedas" })).toBe("/monitors/3/quedas");
    expect(parseRoute("/monitors/3/avisos")).toEqual({ name: "monitor", id: 3, tab: "avisos" });
  });

  it("leva a falha em destaque na âncora", () => {
    const rota = { name: "monitor" as const, id: 3, anchor: "falha-1761650580000" };

    expect(href(rota)).toBe("/monitors/3#falha-1761650580000");
    expect(parseRoute("/monitors/3", "#falha-1761650580000")).toEqual(rota);
  });

  it("não confunde edição com aba", () => {
    expect(parseRoute("/monitors/3/edit")).toEqual({ name: "monitor-edit", id: 3 });
  });
});
