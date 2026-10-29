import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Monitor, Status, Summary } from "../api/types";
import { Board, rotuloDosDependentes } from "./Board";

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

  it("mostra o calado pelo pai em cinza, dizendo quem caiu, e não como problema próprio", async () => {
    // Caso real: a VM da sonda perdeu a saída, e as verificações dela
    // apareciam vermelhas como se cada serviço tivesse caído.
    const calado = { ...resumo(3, "down"), affected_by: { id: 9, name: "sonda vm-01" } };
    vi.mocked(api.summaries).mockResolvedValue({
      items: [resumo(1, "up"), resumo(2, "up"), calado, resumo(4, "up"), resumo(5, "down")],
      from: "",
      to: "",
    });
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);

    expect(await screen.findByText("sem sinal — sonda vm-01 fora")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /fora do ar/ })).not.toBeInTheDocument();
  });

  it("diz quem já está olhando a queda", async () => {
    const visto = { ...resumo(3, "down"), acknowledgement: { by: "ana", at: new Date().toISOString() } };
    vi.mocked(api.summaries).mockResolvedValue({
      items: [resumo(1, "up"), resumo(2, "up"), visto, resumo(4, "up"), resumo(5, "down")],
      from: "",
      to: "",
    });
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);

    expect(await screen.findByText(/visto por ana há/)).toBeInTheDocument();
  });

  it("não oferece filtro de estado que não tem ninguém", async () => {
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("bff");

    expect(screen.queryByRole("button", { name: /degradado/ })).not.toBeInTheDocument();
  });
});

describe("Board: dependentes", () => {
  // A sonda da VM e duas verificações que ela faz, no mesmo grupo.
  function comSonda(calados: boolean) {
    const sonda = { ...monitor(9, "sonda vm-01"), type: "push" as const };
    vi.mocked(api.listMonitors).mockResolvedValue({
      items: [
        monitor(1, "api"),
        { ...monitor(2, "fila"), parent_id: 9 },
        sonda,
        { ...monitor(4, "worker"), parent_id: 9 },
      ],
      has_more: false,
    });
    const ref = calados ? { affected_by: { id: 9, name: "sonda vm-01" } } : {};
    vi.mocked(api.summaries).mockResolvedValue({
      items: [
        resumo(1, "up"),
        { ...resumo(2, "up"), ...ref },
        resumo(9, calados ? "down" : "up"),
        { ...resumo(4, calados ? "down" : "up"), ...ref },
      ],
      from: "",
      to: "",
    });
  }

  it("põe cada dependente recuado sob o pai, que recolhe", async () => {
    comSonda(false);
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("api");

    expect(nomes()).toEqual(["api", "sonda vm-01", "fila", "worker"]);
    expect(screen.getByText("fila").parentElement).toHaveAttribute("style", expect.stringContaining("padding-left: 1.25rem"));

    fireEvent.click(screen.getByRole("button", { name: /2 dependentes de sonda vm-01/ }));
    expect(nomes()).toEqual(["api", "sonda vm-01"]);
  });

  it("com o pai sem sinal, junta os filhos sob ele numa linha só, e o pai sobe", async () => {
    comSonda(true);
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("api");

    const linha = screen.getByRole("button", { name: /2 sem sinal \(sonda vm-01 calada\)/ });
    expect(linha).toHaveAttribute("aria-expanded", "false");
    expect(nomes()).toEqual(["sonda vm-01", "api"]);

    // Abertos, a linha de cima já disse por que estão sem sinal.
    fireEvent.click(linha);
    expect(nomes()).toEqual(["sonda vm-01", "fila", "worker", "api"]);
    expect(screen.queryByText("sem sinal — sonda vm-01 fora")).not.toBeInTheDocument();
  });

  it("com a busca escondendo o pai, o dependente achado aparece sozinho", async () => {
    comSonda(false);
    render(<Board podeEscrever={false} group={{ id: 3, path: "RPA / prod" }} />);
    await screen.findByText("api");

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "worker" } });
    expect(nomes()).toEqual(["worker"]);
  });
});

describe("rotuloDosDependentes", () => {
  const banco = monitor(7, "banco");
  const sonda = { ...monitor(9, "vm-01"), type: "push" as const };

  it("conta os dependentes quando eles têm sinal", () => {
    expect(rotuloDosDependentes(banco, [{}], false)).toBe("1 dependente");
  });

  it("chama de sonda o sinal de vida que parou, e de fora o resto", () => {
    expect(rotuloDosDependentes(sonda, [{ affectedBy: { id: 9 } }], true)).toBe("1 sem sinal (sonda vm-01 calada)");
    expect(rotuloDosDependentes(banco, [{ affectedBy: { id: 7 } }], true)).toBe("1 sem sinal (banco fora)");
    // Calados por um ancestral mais acima, a linha diz qual.
    expect(rotuloDosDependentes(banco, [{ affectedBy: { id: 1, name: "host" } }], true)).toBe("1 sem sinal (host fora)");
  });
});
