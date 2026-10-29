import { describe, expect, it } from "vitest";
import { herdado, lerLembrete, opcoesLembrete, valorLembrete, vistoPor } from "./reminders";

describe("intervalo de lembrete", () => {
  it("separa herdar de desligado", () => {
    // Herdar e desligar parecem a mesma coisa num monitor sem grupo, mas
    // não são: trocar um pelo outro desliga o lembrete que o sistema
    // inteiro tinha ligado.
    expect(lerLembrete("")).toBeNull();
    expect(lerLembrete("0")).toBe(0);
    expect(valorLembrete(null)).toBe("");
    expect(valorLembrete(0)).toBe("0");
    expect(lerLembrete(valorLembrete(30))).toBe(30);
  });

  it("mantém na lista o valor gravado fora dela", () => {
    // Gravado pela API com 45 minutos, sumir do Select faria a próxima
    // edição pela tela trocar o intervalo sem ninguém pedir.
    const valores = opcoesLembrete(45, "herdar").map((o) => o.value);
    expect(valores).toContain("45");
    expect(valores.indexOf("45")).toBeLessThan(valores.indexOf("60"));
  });

  it("começa por herdar e desligar", () => {
    const opcoes = opcoesLembrete(null, "herdar do grupo");
    expect(opcoes[0]).toEqual({ value: "", label: "herdar do grupo" });
    expect(opcoes[1]?.value).toBe("0");
    expect(opcoes.find((o) => o.value === "60")?.label).toBe("a cada 1 h");
  });
});

describe("vistoPor", () => {
  it("diz quem e há quanto tempo", () => {
    const agora = new Date("2026-07-28T12:30:00Z");
    expect(vistoPor({ by: "ana", at: "2026-07-28T12:00:00Z" }, agora)).toBe("visto por ana há 30 min");
  });
});

describe("herdado", () => {
  const grupos = [
    { id: 1, name: "RPA", slug: "rpa", parent_id: null, path: "RPA", reminder_minutes: 60, created_at: "", updated_at: "" },
    { id: 2, name: "prod", slug: "prod", parent_id: 1, path: "RPA / prod", reminder_minutes: null, created_at: "", updated_at: "" },
    { id: 3, name: "hml", slug: "hml", parent_id: 1, path: "RPA / hml", reminder_minutes: 0, created_at: "", updated_at: "" },
  ];

  it("diz de onde vem e quanto vale", () => {
    // "Herdar" sem dizer o quê é escolha às cegas.
    expect(herdado(2, grupos)).toBe("herdar de RPA (a cada 1 h)");
    expect(herdado(3, grupos)).toBe("herdar de hml (desligado)");
    expect(herdado(null, grupos)).toBe("herdar da instalação");
  });
});
