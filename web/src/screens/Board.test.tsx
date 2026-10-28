import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Monitor, Status, Summary } from "../api/types";
import { Board } from "./Board";

/**
 * A tela do grupo: a lista de sempre, recortada pelo grupo, com o
 * problema no topo, busca e filtro por estado.
 */

vi.mock("../api/client", () => ({
  api: { listMonitors: vi.fn(), summaries: vi.fn() },
}));

const { api } = await import("../api/client");

function monitor(id: number, name: string, enabled = true): Monitor {
  return {
    id,
    name,
    type: "http",
    target: `http://${name.replace(/\s/g, "-")}:3000`,
    interval_seconds: 20,
    timeout_seconds: 5,
    confirmation_threshold: 3,
    degraded_latency_ms: 0,
    group_id: 3,
    enabled,
    tags: [],
    created_at: "",
    updated_at: "",
  };
}

function resumo(id: number, status: Status): Summary {
  return {
    monitor_id: id,
    status,
    source: "raw",
    uptime_percent: 100,
    latency_p50_ms: 1,
    latency_p95_ms: 1,
    latency_p99_ms: 1,
    last_check_at: null,
    last_check_status: status,
    up: 0,
    degraded: 0,
    down: 0,
    unknown: 0,
    series: [],
  };
}

beforeEach(() => {
  vi.mocked(api.listMonitors).mockResolvedValue({
    items: [
      monitor(1, "api"),
      monitor(2, "rpa-prod scheduler"),
      monitor(3, "bff"),
      monitor(4, "worker default"),
      monitor(5, "legado", false),
    ],
    has_more: false,
  });
  vi.mocked(api.summaries).mockResolvedValue({
    items: [
      resumo(1, "up"),
      resumo(2, "up"),
      resumo(3, "down"),
      resumo(4, "unknown"),
      resumo(5, "down"),
    ],
    from: "",
    to: "",
  });
});

const nomes = () =>
  screen.getAllByRole("link").map((l) => l.querySelector("span.font-medium")?.textContent);

describe("Board num grupo", () => {
  it("pede só o grupo ao servidor e põe o problema no topo, pausado no fim", async () => {
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);

    await screen.findByText("bff");
    expect(api.listMonitors).toHaveBeenCalledWith(expect.objectContaining({ group_id: 3 }));
    expect(nomes()).toEqual(["bff", "worker default", "api", "scheduler", "legado"]);
  });

  it("encurta o nome que repete o grupo e mostra o grupo como título", async () => {
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);

    expect(await screen.findByText("scheduler")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("RPA · prod");
  });

  it("busca por nome e filtra por estado", async () => {
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("bff");

    fireEvent.change(screen.getByRole("searchbox", { name: "buscar por nome ou alvo" }), {
      target: { value: "WORKER" },
    });
    expect(nomes()).toEqual(["worker default"]);

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /fora do ar/ }));
    // O pausado tinha caído antes da pausa, mas não é problema de agora.
    expect(nomes()).toEqual(["bff"]);
  });

  it("não oferece filtro de estado que não tem ninguém", async () => {
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("bff");

    expect(screen.queryByRole("button", { name: /degradado/ })).not.toBeInTheDocument();
  });
});
