import { describe, expect, it } from "vitest";
import { defaultChoice, expiresAtFor, maxCustomDate, validityOptions } from "./token-expiry";

const semTeto = { require_expiry: false, max_lifetime_seconds: 0 };
const DIA = 86400;

describe("validityOptions", () => {
  it("oferece os presets, data personalizada e sem expiração quando não há teto nem exigência", () => {
    expect(validityOptions(semTeto).map((o) => o.value)).toEqual([
      "7d",
      "30d",
      "90d",
      "custom",
      "none",
    ]);
  });

  it("esconde os presets acima do teto", () => {
    const pol = { require_expiry: false, max_lifetime_seconds: 30 * DIA };
    expect(validityOptions(pol).map((o) => o.value)).toEqual(["7d", "30d", "custom", "none"]);
  });

  it("omite 'sem expiração' quando a instalação exige validade", () => {
    const pol = { require_expiry: true, max_lifetime_seconds: 0 };
    expect(validityOptions(pol).map((o) => o.value)).not.toContain("none");
  });
});

describe("defaultChoice", () => {
  it("prefere um prazo finito, nunca 'sem expiração' por omissão", () => {
    expect(defaultChoice(semTeto)).toBe("30d");
  });

  it("cai para um preset menor quando o teto corta os maiores", () => {
    expect(defaultChoice({ require_expiry: true, max_lifetime_seconds: 7 * DIA })).toBe("7d");
  });
});

describe("expiresAtFor", () => {
  const agora = new Date("2026-11-11T12:00:00Z");

  it("sem expiração vira null", () => {
    expect(expiresAtFor("none", "", agora)).toBeNull();
  });

  it("um preset soma os dias a agora", () => {
    expect(expiresAtFor("30d", "", agora)).toBe(new Date("2026-12-11T12:00:00Z").toISOString());
  });

  it("a data personalizada cai no fim do dia escolhido", () => {
    const got = expiresAtFor("custom", "2026-12-01", agora);
    expect(got).not.toBeNull();
    const t = new Date(got as string).getTime();
    expect(t).toBeGreaterThan(new Date("2026-12-01T00:00:00").getTime());
    expect(t).toBeLessThan(new Date("2026-12-02T12:00:00").getTime());
  });
});

describe("maxCustomDate", () => {
  it("é vazio sem teto", () => {
    expect(maxCustomDate(semTeto)).toBe("");
  });

  it("é a data do teto contado a partir de agora", () => {
    const agora = new Date("2026-11-11T12:00:00Z");
    expect(maxCustomDate({ require_expiry: false, max_lifetime_seconds: 30 * DIA }, agora)).toBe(
      "2026-12-11",
    );
  });
});
