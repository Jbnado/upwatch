import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Monitor, MonitorEvent, Problem, Summary, User } from "../api/types";
import { MonitorDetail } from "./MonitorDetail";

/**
 * O detalhe do monitor.
 *
 * O que se prende aqui: o quadro de estado existe só com problema, a aba
 * de eventos não contradiz o quadro, a falha da âncora aparece em
 * destaque e as abas têm a semântica de abas.
 */

vi.mock("../api/client", () => ({
  api: {
    getMonitor: vi.fn(),
    summaries: vi.fn(),
    listGroups: vi.fn(),
    problems: vi.fn(),
    heartbeats: vi.fn(),
    events: vi.fn(),
    incidents: vi.fn(),
    monitorChannels: vi.fn(),
    groupChannels: vi.fn(),
    listChannels: vi.fn(),
    exportUrl: () => "/export",
  },
}));

const { api } = await import("../api/client");

const EU: User = {
  id: 1,
  username: "ana",
  role: "viewer",
  scope: { all_groups: true, group_ids: [] },
  created_at: "",
  updated_at: "",
};

const BFF: Monitor = {
  id: 12,
  name: "bff",
  type: "http",
  target: "http://bff:3000/health",
  interval_seconds: 20,
  timeout_seconds: 5,
  confirmation_threshold: 3,
  degraded_latency_ms: 0,
  group_id: 3,
  enabled: true,
  tags: [],
  created_at: "",
  updated_at: "",
};

function resumo(status: Summary["status"]): Summary {
  return {
    monitor_id: 12,
    status,
    source: "raw",
    uptime_percent: 99.2,
    latency_p50_ms: 120,
    latency_p95_ms: 180,
    latency_p99_ms: 300,
    last_check_at: null,
    up: 0,
    degraded: 0,
    down: 0,
    unknown: 0,
    series: [],
  };
}

const QUEDA: Problem = {
  monitor_id: 12,
  name: "bff",
  type: "http",
  group_id: 3,
  group_path: "RPA / prod",
  status: "down",
  since: new Date(Date.now() - 12 * 60_000).toISOString(),
  cause: "connection refused",
  last_check_at: "2026-10-28T11:43:00Z",
};

const FALHA: MonitorEvent = {
  type: "failure",
  at: "2026-10-28T11:43:00Z",
  status: "down",
  message: "connection refused",
};

function cenario({
  status,
  problema,
  eventos,
}: {
  status: Summary["status"];
  problema: Problem | null;
  eventos: MonitorEvent[];
}) {
  vi.mocked(api.getMonitor).mockResolvedValue(BFF);
  vi.mocked(api.summaries).mockResolvedValue({ items: [resumo(status)], from: "", to: "" });
  vi.mocked(api.listGroups).mockResolvedValue({
    items: [
      { id: 1, name: "RPA", slug: "rpa", parent_id: null, path: "RPA", created_at: "", updated_at: "" },
      { id: 3, name: "prod", slug: "prod", parent_id: 1, path: "RPA / prod", created_at: "", updated_at: "" },
    ],
  });
  vi.mocked(api.problems).mockResolvedValue({ items: problema ? [problema] : [] });
  vi.mocked(api.heartbeats).mockResolvedValue({
    items: [{ status: "up" }, { status: "down" }, { status: "down" }, { status: "down" }].map(
      (b, i) => ({ ...b, timestamp: String(i), latency_ms: 0, probe_id: "local" }),
    ) as never,
  });
  vi.mocked(api.events).mockResolvedValue({ items: eventos, has_more: false });
}

beforeEach(() => {
  vi.mocked(api.incidents).mockResolvedValue({ items: [], has_more: false });
  vi.mocked(api.monitorChannels).mockResolvedValue({ items: [] });
  vi.mocked(api.groupChannels).mockResolvedValue({ items: [] });
});

describe("MonitorDetail", () => {
  it("abre o quadro de estado com tempo, causa e falhas seguidas quando está fora do ar", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [FALHA] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const quadro = await screen.findByRole("region", { name: "estado atual" });
    expect(quadro).toHaveTextContent("fora do ar há 12 min");
    expect(quadro).toHaveTextContent("conexão recusada");
    expect(within(quadro).getByTitle("connection refused")).toBeInTheDocument();
    expect(await within(quadro).findByText(/3 falhas seguidas · desde/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /RPA · prod/ })).toBeInTheDocument();
  });

  it("não tem quadro de estado com o monitor no ar", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    expect(await screen.findByText("Nenhuma falha nem mudança de estado registrada.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "estado atual" })).not.toBeInTheDocument();
  });

  it("não diz que nada mudou quando está fora do ar e a linha do tempo está vazia", async () => {
    // O defeito da #49: "Nenhuma mudança no período" sob um monitor caído.
    cenario({ status: "down", problema: QUEDA, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    expect(await screen.findByText(/O monitor está fora do ar/)).toBeInTheDocument();
    expect(screen.queryByText(/Nenhuma mudança no período/)).not.toBeInTheDocument();
  });

  it("destaca a falha que veio na âncora do endereço", async () => {
    const ancora = `falha-${Date.parse(FALHA.at)}`;
    cenario({
      status: "down",
      problema: QUEDA,
      eventos: [FALHA, { ...FALHA, at: "2026-10-28T11:42:40Z" }],
    });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    const linhas = await screen.findAllByRole("listitem");
    const destaque = linhas.filter((l) => l.getAttribute("aria-current") === "true");
    expect(destaque).toHaveLength(1);
    expect(destaque[0]).toHaveAttribute("id", ancora);
    expect(destaque[0]).toHaveTextContent("do link");
  });

  it("tem abas de verdade, com a de eventos selecionada e as setas andando", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const lista = await screen.findByRole("tablist");
    const abas = within(lista).getAllByRole("tab");
    expect(abas.map((a) => a.textContent)).toEqual(["Eventos", "Quedas", "Avisos"]);
    expect(abas[0]).toHaveAttribute("aria-selected", "true");
    expect(abas[1]).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", abas[0]!.id);

    fireEvent.keyDown(abas[0]!, { key: "ArrowRight" });
    expect(window.location.pathname).toBe("/monitors/12/quedas");
  });

  it("esconde editar e remover de quem só observa", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("tablist");
    expect(screen.queryByRole("button", { name: "Editar" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Mais ações" }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "Exportar CSV (24 h)",
      "Exportar JSON (24 h)",
    ]);
  });
});

describe("MonitorDetail: falhas repetidas", () => {
  const recusada = (i: number): MonitorEvent => ({
    type: "failure",
    at: new Date(Date.UTC(2026, 9, 28, 11, 43, 0) - i * 20_000).toISOString(),
    status: "down",
    message: `dial tcp 127.0.0.1:1: connectex: Nenhuma conexão pôde ser feita porque a máquina de destino as recusou ativamente. (${i})`,
  });
  const nove = Array.from({ length: 9 }, (_, i) => recusada(i));

  it("junta falhas iguais numa linha que abre para as individuais", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const linha = await screen.findByRole("button", { name: /conexão recusada.*9 vezes/ });
    expect(linha).toHaveAttribute("aria-expanded", "false");
    // Fechado, nenhuma das nove aparece com o texto técnico.
    expect(screen.queryByText(/connectex/)).not.toBeInTheDocument();

    fireEvent.click(linha);

    expect(linha).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByText(/connectex/)).toHaveLength(9);
  });

  it("abre o grupo que contém a falha do link, com ela marcada", async () => {
    const alvo = nove[4]!;
    const ancora = `falha-${Date.parse(alvo.at)}`;
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    const linha = await screen.findByRole("button", { name: /9 vezes/ });
    expect(linha).toHaveAttribute("aria-expanded", "true");
    const marcada = document.getElementById(ancora);
    expect(marcada).toHaveAttribute("aria-current", "true");
    expect(marcada).toHaveTextContent("do link");
  });

  it("mostra o texto técnico de uma falha solta ao abrir a linha", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [FALHA] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const linha = await screen.findByRole("button", { name: /conexão recusada/ });
    const painel = screen.getByRole("tabpanel");
    expect(within(painel).queryByText("connection refused")).not.toBeInTheDocument();

    fireEvent.click(linha);

    expect(within(painel).getByText("connection refused")).toBeInTheDocument();
  });

  it("une o grupo que atravessa a página ao carregar os mais antigos", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [] });
    vi.mocked(api.events)
      .mockResolvedValueOnce({ items: nove.slice(0, 5), has_more: true, next_before: nove[4]!.at })
      .mockResolvedValueOnce({ items: nove.slice(5), has_more: false });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("button", { name: /5 vezes/ });
    fireEvent.click(screen.getByRole("button", { name: "Carregar mais antigos" }));

    expect(await screen.findByRole("button", { name: /9 vezes/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /4 vezes/ })).not.toBeInTheDocument();
  });
});
