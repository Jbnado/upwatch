import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import { href, legacyRedirect, parseRoute, useRoute } from "./router";

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

describe("rotas dos ajustes", () => {
  it("dá endereço próprio a cada seção", () => {
    // O endereço é o que se cola no chat: "olha os canais" precisa abrir
    // nos canais, não no topo de uma página comprida.
    expect(href({ name: "ajustes", section: "canais" })).toBe("/ajustes/canais");
    expect(parseRoute("/ajustes/canais")).toEqual({ name: "ajustes", section: "canais" });
    expect(parseRoute("/ajustes/tokens")).toEqual({ name: "ajustes", section: "tokens" });
  });

  it("abre o menu em /ajustes sem seção", () => {
    expect(href({ name: "ajustes" })).toBe("/ajustes");
    expect(parseRoute("/ajustes")).toEqual({ name: "ajustes" });
  });

  it("trata seção desconhecida como o menu", () => {
    expect(parseRoute("/ajustes/inexistente")).toEqual({ name: "ajustes" });
  });

  it("põe a página de estado sob a seção de páginas", () => {
    expect(href({ name: "ajustes-pagina", id: 4 })).toBe("/ajustes/paginas/4");
    expect(parseRoute("/ajustes/paginas/4")).toEqual({ name: "ajustes-pagina", id: 4 });
    expect(parseRoute("/ajustes/paginas/abc")).toEqual({ name: "ajustes", section: "paginas" });
  });
});

describe("endereços antigos dos ajustes", () => {
  // Quem guardou /settings ou /status-pages nos favoritos, ou colou o link
  // numa issue, precisa cair no lugar equivalente, e não no painel.
  it("leva /settings para /ajustes", () => {
    expect(legacyRedirect("/settings")).toBe("/ajustes");
    expect(parseRoute("/settings")).toEqual({ name: "ajustes" });
  });

  it("leva /status-pages para a seção de páginas", () => {
    expect(legacyRedirect("/status-pages")).toBe("/ajustes/paginas");
    expect(legacyRedirect("/status-pages/")).toBe("/ajustes/paginas");
    expect(parseRoute("/status-pages")).toEqual({ name: "ajustes", section: "paginas" });
  });

  it("leva /status-pages/:id para a mesma página na nova casa", () => {
    expect(legacyRedirect("/status-pages/7")).toBe("/ajustes/paginas/7");
    expect(parseRoute("/status-pages/7")).toEqual({ name: "ajustes-pagina", id: 7 });
  });

  it("não mexe em endereço atual", () => {
    expect(legacyRedirect("/ajustes/canais")).toBeNull();
    expect(legacyRedirect("/monitors/3")).toBeNull();
    // A página pública não é ajuste: /status fica onde está.
    expect(legacyRedirect("/status/estado")).toBeNull();
  });
});

describe("useRoute com endereço antigo", () => {
  it("troca a barra pelo endereço novo sem empilhar histórico", () => {
    window.history.replaceState({}, "", "/status-pages/7");
    const antes = window.history.length;

    const { result } = renderHook(() => useRoute());

    expect(result.current).toEqual({ name: "ajustes-pagina", id: 7 });
    expect(window.location.pathname).toBe("/ajustes/paginas/7");
    expect(window.history.length).toBe(antes);
  });
});
