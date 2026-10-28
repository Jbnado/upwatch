import { describe, expect, it } from "vitest";
import type { User } from "../api/types";
import { firstSection, sectionAccess, settingsMenu } from "./settings-sections";

/**
 * Quais seções de ajustes cada conta vê.
 *
 * O menu oferece só o que o servidor aceita daquela conta: um item que
 * leva a uma tela onde tudo devolve "sem permissão" ensina a desconfiar
 * do resto do menu. A barreira continua no servidor — aqui é só o que
 * mostrar, e precisa ser fiel ao que ele aplica.
 */

function conta(role: User["role"], all_groups: boolean): User {
  return {
    id: 1,
    username: "ana",
    role,
    scope: { all_groups, group_ids: all_groups ? [] : [4] },
    created_at: "",
    updated_at: "",
  };
}

const ids = (u: User) => settingsMenu(u).flatMap((g) => g.items.map((i) => i.id));

describe("menu dos ajustes", () => {
  it("dá tudo a quem administra todos os grupos", () => {
    expect(ids(conta("admin", true))).toEqual([
      "grupos",
      "contas",
      "canais",
      "paginas",
      "tokens",
      "senha",
    ]);
  });

  it("tira canais e páginas de estado de quem administra só alguns grupos", () => {
    // Canal e página pública valem para todos os grupos, e o servidor
    // recusa a escrita deles a quem não alcança todos. Token não: ele é
    // da própria conta e herda o escopo dela, então o servidor o emite.
    expect(ids(conta("admin", false))).toEqual(["grupos", "contas", "tokens", "senha"]);
  });

  it("deixa ao observador só a própria senha", () => {
    expect(ids(conta("viewer", true))).toEqual(["senha"]);
    expect(ids(conta("viewer", false))).toEqual(["senha"]);
  });

  it("não deixa título de grupo sem item embaixo", () => {
    // Um "AVISOS" solto, sem nada abaixo, parece um menu quebrado.
    const titulos = settingsMenu(conta("viewer", true)).map((g) => g.title);
    expect(titulos).toEqual(["Minha conta"]);
  });
});

describe("acesso direto pela rota", () => {
  it("abre a seção que o menu oferece", () => {
    expect(sectionAccess(conta("admin", true), "canais")).toEqual({ ok: true });
  });

  it("explica a recusa de escopo em vez de mostrar uma tela vazia", () => {
    const r = sectionAccess(conta("admin", false), "paginas");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.reason).toMatch(/todos os grupos/);
  });

  it("explica a recusa de papel ao observador", () => {
    const r = sectionAccess(conta("viewer", true), "grupos");
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.reason).toMatch(/administr/);
  });
});

describe("seção inicial", () => {
  it("é a primeira do menu da conta", () => {
    expect(firstSection(conta("admin", true))).toBe("grupos");
    expect(firstSection(conta("viewer", false))).toBe("senha");
  });
});
