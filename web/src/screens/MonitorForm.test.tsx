import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Group, Monitor } from "../api/types";
import { MonitorForm } from "./MonitorForm";

/**
 * O pai no cadastro. O PUT sobrescreve o monitor inteiro, então o
 * formulário que não carrega o pai o apaga a cada edição — e a
 * verificação da sonda volta a avisar sozinha na próxima queda dela.
 */

vi.mock("../api/client", () => ({
  api: {
    listGroups: vi.fn(),
    listMonitors: vi.fn(),
    getMonitor: vi.fn(),
    createMonitor: vi.fn(),
    updateMonitor: vi.fn(),
  },
}));
vi.mock("../lib/router", () => ({ navigate: vi.fn() }));

const { api } = await import("../api/client");

const SONDAS: Group = { id: 1, name: "Sondas", slug: "sondas", parent_id: null, path: "Sondas", created_at: "", updated_at: "" };
const PROD: Group = { id: 2, name: "prod", slug: "prod", parent_id: null, path: "prod", created_at: "", updated_at: "" };

function monitor(id: number, over: Partial<Monitor>): Monitor {
  return {
    id,
    name: `m${id}`,
    type: "http",
    target: "https://exemplo.com",
    interval_seconds: 60,
    timeout_seconds: 10,
    confirmation_threshold: 3,
    degraded_latency_ms: 0,
    enabled: true,
    tags: [],
    created_at: "",
    updated_at: "",
    ...over,
  };
}

const VIDA = monitor(1, { name: "sonda vm-01", type: "push", group_id: 1, config: { token: "x".repeat(20) } });
const API_PROD = monitor(3, { name: "api", group_id: 2 });
const REDIS = monitor(2, { name: "redis", group_id: 2, parent_id: 1 });

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.listGroups).mockResolvedValue({ items: [SONDAS, PROD] });
  vi.mocked(api.listMonitors).mockResolvedValue({ items: [VIDA, REDIS, API_PROD], has_more: false });
  vi.mocked(api.getMonitor).mockResolvedValue(REDIS);
  vi.mocked(api.updateMonitor).mockResolvedValue(REDIS);
});

describe("MonitorForm e o pai", () => {
  it("mantém o pai ao salvar uma edição", async () => {
    render(<MonitorForm id={2} />);
    const pai = await screen.findByRole("combobox", { name: /depende de/ });
    await waitFor(() => expect(pai).toHaveValue("1"));

    fireEvent.click(screen.getByRole("button", { name: "Salvar alterações" }));

    await waitFor(() => expect(api.updateMonitor).toHaveBeenCalled());
    expect(vi.mocked(api.updateMonitor).mock.lastCall?.[1]).toMatchObject({ parent_id: 1 });
  });

  it("sugere primeiro o sinal de vida das sondas, sem oferecer o próprio monitor", async () => {
    render(<MonitorForm id={2} />);
    const pai = await screen.findByRole("combobox", { name: /depende de/ });
    await waitFor(() => expect(pai).toHaveValue("1"));

    const sugestao = within(pai).getByRole("group", { name: /sondas/ });
    expect(within(sugestao).getByRole("option", { name: /sonda vm-01/ })).toBeInTheDocument();
    expect(within(pai).queryByRole("option", { name: /redis/ })).not.toBeInTheDocument();
  });

  it("desliga o pai com 'nenhum'", async () => {
    render(<MonitorForm id={2} />);
    const pai = await screen.findByRole("combobox", { name: /depende de/ });
    await waitFor(() => expect(pai).toHaveValue("1"));

    fireEvent.change(pai, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Salvar alterações" }));

    await waitFor(() => expect(api.updateMonitor).toHaveBeenCalled());
    expect(vi.mocked(api.updateMonitor).mock.lastCall?.[1]).toMatchObject({ parent_id: null });
  });
});

/**
 * O lembrete. O PUT sobrescreve, e omitir volta a herdar: o formulário que
 * não carrega o intervalo religaria, a cada edição, o lembrete que alguém
 * desligou de propósito.
 */
describe("MonitorForm e o lembrete", () => {
  it("mantém o desligado ao salvar uma edição", async () => {
    vi.mocked(api.getMonitor).mockResolvedValue({ ...REDIS, reminder_minutes: 0 });
    render(<MonitorForm id={2} />);
    const campo = await screen.findByRole("combobox", { name: /relembrar queda aberta/ });
    await waitFor(() => expect(campo).toHaveValue("0"));

    fireEvent.click(screen.getByRole("button", { name: "Salvar alterações" }));

    await waitFor(() => expect(api.updateMonitor).toHaveBeenCalled());
    expect(vi.mocked(api.updateMonitor).mock.lastCall?.[1]).toMatchObject({ reminder_minutes: 0 });
  });

  it("diz o que herdar vai dar", async () => {
    vi.mocked(api.listGroups).mockResolvedValue({ items: [SONDAS, { ...PROD, reminder_minutes: 30 }] });
    render(<MonitorForm id={2} />);
    const campo = await screen.findByRole("combobox", { name: /relembrar queda aberta/ });

    await waitFor(() =>
      expect(within(campo).getByRole("option", { name: "herdar de prod (a cada 30 min)" })).toBeInTheDocument(),
    );
    fireEvent.change(campo, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Salvar alterações" }));

    await waitFor(() => expect(api.updateMonitor).toHaveBeenCalled());
    expect(vi.mocked(api.updateMonitor).mock.lastCall?.[1]).toMatchObject({ reminder_minutes: null });
  });
});
