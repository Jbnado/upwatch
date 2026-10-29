import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Group, MaintenanceWindow, Monitor, PublicView } from "../api/types";
import { Maintenance } from "./Maintenance";
import { Status } from "./Status";

vi.mock("../api/client", () => ({
  api: {
    maintenanceWindows: vi.fn(),
    createMaintenance: vi.fn(),
    updateMaintenance: vi.fn(),
    endMaintenance: vi.fn(),
    deleteMaintenance: vi.fn(),
    listGroups: vi.fn(),
    listMonitors: vi.fn(),
    publicStatus: vi.fn(),
    feedUrl: vi.fn(() => "/feed"),
  },
}));

const { api } = await import("../api/client");

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

const GRUPOS = [grupo(10, "RPA"), grupo(11, "RPA / hml", 10), grupo(12, "RPA / prod", 10)];
const MONITORES = [monitor(7, "erp", 12), monitor(9, "api", 12), monitor(8, "api", 11)];

function janela(parcial: Partial<MaintenanceWindow>): MaintenanceWindow {
  return {
    id: 1,
    title: "deploy do RPA",
    starts_at: "2026-10-29T22:00:00Z",
    ends_at: "2026-10-29T22:30:00Z",
    recurrence: "none",
    time_zone: "America/Sao_Paulo",
    monitor_ids: [],
    group_ids: [12],
    state: "active",
    next: { start: "2026-10-29T22:00:00Z", end: "2026-10-29T22:30:00Z" },
    ...parcial,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.listGroups).mockResolvedValue({ items: GRUPOS });
  vi.mocked(api.listMonitors).mockResolvedValue({ items: MONITORES, has_more: false } as never);
  vi.mocked(api.maintenanceWindows).mockResolvedValue({ items: [] });
  vi.mocked(api.createMaintenance).mockResolvedValue(janela({}));
  vi.mocked(api.endMaintenance).mockResolvedValue(janela({ state: "finished" }));
});

describe("Maintenance", () => {
  it("declara a janela no grupo, que cobre os monitores criados depois", async () => {
    render(<Maintenance />);
    await screen.findByText("Nenhuma janela de manutenção.");

    fireEvent.click(screen.getByRole("button", { name: "+ Nova janela" }));
    fireEvent.change(screen.getByPlaceholderText("deploy do RPA"), { target: { value: "deploy das 19h" } });
    fireEvent.change(screen.getByLabelText("início"), { target: { value: "2026-10-29T19:00" } });
    fireEvent.change(screen.getByLabelText("fim"), { target: { value: "2026-10-29T19:30" } });
    fireEvent.click(await screen.findByRole("checkbox", { name: /^prod.*RPA/ }));
    fireEvent.click(screen.getByRole("button", { name: "Declarar janela" }));

    await waitFor(() => expect(api.createMaintenance).toHaveBeenCalledTimes(1));
    const enviado = vi.mocked(api.createMaintenance).mock.calls[0]![0];
    // O grupo vai como grupo, e não como a lista de monitores dele hoje:
    // o monitor cadastrado amanhã no RPA/prod também fica coberto.
    expect(enviado).toMatchObject({
      title: "deploy das 19h",
      recurrence: "none",
      group_ids: [12],
      monitor_ids: [],
      starts_at: new Date("2026-10-29T19:00").toISOString(),
      ends_at: new Date("2026-10-29T19:30").toISOString(),
    });
  });

  it("grava monitor avulso quando o ambiente fica só em parte", async () => {
    render(<Maintenance />);
    fireEvent.click(await screen.findByRole("button", { name: "+ Nova janela" }));

    fireEvent.change(screen.getByPlaceholderText("deploy do RPA"), { target: { value: "deploy do erp" } });
    fireEvent.click(await screen.findByRole("checkbox", { name: /^prod.*RPA/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /^erp.*RPA · prod/ }));
    fireEvent.click(screen.getByRole("button", { name: "Declarar janela" }));

    await waitFor(() => expect(api.createMaintenance).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.createMaintenance).mock.calls[0]![0]).toMatchObject({
      group_ids: [],
      monitor_ids: [9],
    });
  });

  it("edita sem perder o grupo alvo e reabre com os monitores dele marcados", async () => {
    vi.mocked(api.maintenanceWindows).mockResolvedValue({ items: [janela({ group_ids: [10] })] });
    vi.mocked(api.updateMaintenance).mockResolvedValue(janela({}));
    render(<Maintenance />);

    const linha = (await screen.findByText("deploy do RPA")).closest("li")!;
    await within(linha).findByText(/RPA \(todos os ambientes\)/);
    fireEvent.click(within(linha).getByRole("button", { name: "Editar" }));

    expect(await screen.findByRole("checkbox", { name: /^RPA/ })).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Salvar" }));

    await waitFor(() => expect(api.updateMaintenance).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.updateMaintenance).mock.calls[0]![1]).toMatchObject({ group_ids: [10], monitor_ids: [] });
  });

  it("encerra a janela em curso antes do previsto", async () => {
    vi.mocked(api.maintenanceWindows).mockResolvedValue({ items: [janela({})] });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<Maintenance />);

    const linha = (await screen.findByText("deploy do RPA")).closest("li")!;
    expect(within(linha).getByText("em curso")).toBeInTheDocument();
    await within(linha).findByText(/RPA \/ prod/);
    fireEvent.click(within(linha).getByRole("button", { name: "Encerrar agora" }));

    await waitFor(() => expect(api.endMaintenance).toHaveBeenCalledWith(1));
  });
});

describe("Status em manutenção", () => {
  const view: PublicView = {
    slug: "estado",
    title: "Estado",
    status: "unknown",
    impact: "none",
    window_days: 90,
    generated_at: "2026-10-29T22:10:00Z",
    groups: [
      {
        name: "",
        monitors: [
          { name: "API", status: "unknown", history: [], maintenance: true },
          { name: "Site", status: "up", history: [] },
        ],
      },
    ],
    announcements: [],
    maintenances: [
      {
        title: "Atualização do banco",
        message: "Indisponível por até meia hora.",
        starts_at: "2026-10-29T22:00:00Z",
        ends_at: "2026-10-29T22:30:00Z",
        recurrence: "none",
        active: true,
        components: ["API"],
      },
    ],
  };

  // Quem abre o link no meio do deploy precisa ler "manutenção", e não
  // "sem medição" ou queda.
  it("diz manutenção no topo, no componente e com o texto da janela", async () => {
    vi.mocked(api.publicStatus).mockResolvedValue(view);
    render(<Status slug="estado" />);

    expect(await screen.findByText("Manutenção em andamento")).toBeInTheDocument();
    expect(screen.getByText("Em manutenção")).toBeInTheDocument();
    expect(screen.getByText("Atualização do banco")).toBeInTheDocument();
    expect(screen.getByText("Indisponível por até meia hora.")).toBeInTheDocument();
  });

  it("não esconde uma queda de verdade atrás da manutenção", async () => {
    vi.mocked(api.publicStatus).mockResolvedValue({ ...view, status: "down" });
    render(<Status slug="estado" />);

    expect(await screen.findByText("Interrupção em andamento")).toBeInTheDocument();
    expect(screen.queryByText("Manutenção em andamento")).not.toBeInTheDocument();
  });
});
