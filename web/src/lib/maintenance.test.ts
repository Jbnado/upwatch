import { describe, expect, it } from "vitest";
import type { Group, MaintenanceWindow, Monitor } from "../api/types";
import { alvos, doCampo, ordenar, padraoAgora, paraCampo, quando } from "./maintenance";

function janela(parcial: Partial<MaintenanceWindow>): MaintenanceWindow {
  return {
    id: 1,
    title: "deploy",
    starts_at: "2026-10-29T22:00:00Z",
    ends_at: "2026-10-29T22:30:00Z",
    recurrence: "none",
    time_zone: "UTC",
    monitor_ids: [],
    group_ids: [],
    state: "scheduled",
    ...parcial,
  };
}

const grupo = (id: number, path: string, parent_id: number | null = null): Group => ({
  id,
  name: path.split(" / ").pop()!,
  slug: "",
  parent_id,
  path,
  created_at: "",
  updated_at: "",
});

const monitor = (id: number, name: string, group_id: number | null): Monitor => ({
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
});

describe("janelas de manutenção", () => {
  it("põe a em curso primeiro e as encerradas por último", () => {
    const lista = ordenar([
      janela({ id: 1, state: "finished", starts_at: "2026-10-01T00:00:00Z" }),
      janela({ id: 2, state: "scheduled", next: { start: "2026-11-02T00:00:00Z", end: "2026-11-02T01:00:00Z" } }),
      janela({ id: 3, state: "active" }),
      janela({ id: 4, state: "scheduled", next: { start: "2026-10-30T00:00:00Z", end: "2026-10-30T01:00:00Z" } }),
      janela({ id: 5, state: "finished", starts_at: "2026-10-20T00:00:00Z" }),
    ]);
    expect(lista.map((j) => j.id)).toEqual([3, 4, 2, 5, 1]);
  });

  it("lê e escreve o campo de data no horário de parede do navegador", () => {
    // O valor do campo e o instante gravado precisam ser o mesmo momento:
    // "19:00" digitado é 19:00 de quem digitou.
    const campo = "2026-10-29T19:00";
    expect(paraCampo(doCampo(campo))).toBe(campo);
  });

  it("propõe começar agora, por meia hora", () => {
    const { inicio, fim } = padraoAgora(new Date(2026, 9, 29, 18, 57, 42));
    expect(inicio).toBe("2026-10-29T18:57");
    expect(fim).toBe("2026-10-29T19:27");
  });

  it("escreve a recorrente pela próxima ocorrência", () => {
    const j = janela({
      recurrence: "weekly",
      next: { start: "2026-11-01T05:00:00Z", end: "2026-11-01T06:00:00Z" },
    });
    expect(quando(j)).toMatch(/^toda semana · próxima /);
    expect(quando(janela({}))).not.toMatch(/próxima/);
  });

  it("escreve os alvos com o grupo junto, sem número de quem sumiu", () => {
    const grupos = [grupo(10, "RPA"), grupo(12, "RPA / prod", 10)];
    const monitores = [monitor(7, "erp", 12)];
    const j = janela({ group_ids: [10, 12, 99], monitor_ids: [7, 98] });
    expect(alvos(j, grupos, monitores)).toEqual(["RPA (todos os ambientes)", "RPA / prod", "erp · RPA / prod"]);
  });
});
