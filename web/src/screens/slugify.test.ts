import { describe, expect, it } from "vitest";
import { slugify } from "./StatusPages";

/**
 * O slug vai na URL e é permanente na prática: uma vez compartilhado, o
 * endereço circula em chat e em documento, e trocá-lo quebra o link de
 * todo mundo. Vale acertar na primeira digitação.
 */

describe("slugify", () => {
  it("remove acento em vez de escapá-lo", () => {
    // "Situação" viraria "situa%C3%A7%C3%A3o" numa URL — ilegível para
    // colar em qualquer lugar.
    expect(slugify("Situação da Operação")).toBe("situacao-da-operacao");
  });

  it("junta pontuação num hífen só", () => {
    // Sem isto sairia "estado---plataforma", que o servidor recusa por
    // hífen duplo.
    expect(slugify("Estado — a plataforma!")).toBe("estado-a-plataforma");
    expect(slugify("API / Console")).toBe("api-console");
  });

  it("não deixa hífen nas pontas", () => {
    expect(slugify("  Estado  ")).toBe("estado");
    expect(slugify("!Estado!")).toBe("estado");
  });

  it("respeita o limite de comprimento", () => {
    expect(slugify("a".repeat(200)).length).toBe(64);
  });

  it("devolve vazio quando não sobra nada", () => {
    // O campo continua obrigatório no formulário; o que não pode é
    // inventar um endereço a partir de pontuação.
    expect(slugify("!!!")).toBe("");
  });

  it("preserva números", () => {
    expect(slugify("Loja BR 2")).toBe("loja-br-2");
  });
});
