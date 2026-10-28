import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, type Channel, type Group, type GroupSummary, type Scope } from "../api/types";
import { Groups } from "./Groups";

/**
 * A gestão de grupos nos ajustes, vista por quem tem escopo.
 *
 * O servidor recusa com 403 o que a conta restrita não pode: vincular
 * canal a grupo (o canal é de todos os grupos) e renomear grupo cujos
 * irmãos ela não enxerga inteiros (um sistema, ou o ambiente concedido
 * sem o sistema). A tela não oferece o que vai ser recusado, diz por quê,
 * e quando o servidor recusa mesmo assim, mostra a recusa.
 */

vi.mock("../api/client", () => ({
  api: {
    listGroups: vi.fn(),
    groupSummary: vi.fn(),
    groupChannels: vi.fn(),
    listChannels: vi.fn(),
    linkGroupChannel: vi.fn(),
    unlinkGroupChannel: vi.fn(),
    updateMonitorGroup: vi.fn(),
    deleteMonitorGroup: vi.fn(),
    createMonitorGroup: vi.fn(),
  },
}));

const { api } = await import("../api/client");

const RPA: Group = { id: 1, name: "RPA", slug: "rpa", parent_id: null, path: "RPA", created_at: "", updated_at: "" };
const PROD: Group = { id: 3, name: "prod", slug: "prod", parent_id: 1, path: "RPA / prod", created_at: "", updated_at: "" };

const PLANTAO: Channel = { id: 7, name: "plantão", type: "discord", enabled: true, created_at: "", updated_at: "" };

const TOTAL: Scope = { all_groups: true, group_ids: [] };
const RESTRITA: Scope = { all_groups: false, group_ids: [1] };

function resumo(g: Group): GroupSummary {
  return {
    group_id: g.id,
    parent_id: g.parent_id,
    name: g.name,
    path: g.path,
    status: "up",
    total: 1,
    up: 1,
    degraded: 0,
    down: 0,
    unknown: 0,
    paused: 0,
    problems: 0,
  };
}

beforeEach(() => {
  vi.mocked(api.listGroups).mockResolvedValue({ items: [RPA, PROD] });
  vi.mocked(api.groupSummary).mockResolvedValue({ items: [resumo(PROD)] });
  vi.mocked(api.groupChannels).mockResolvedValue({ items: [PLANTAO] });
  vi.mocked(api.listChannels).mockResolvedValue({ items: [PLANTAO] });
});

/** linha é a linha do grupo na árvore, achada pelo nome exibido nela. */
function linha(nome: string) {
  const rotulo = screen.getAllByText(nome).find((e) => e.tagName === "SPAN");
  return rotulo!.closest("div.flex.flex-col") as HTMLElement;
}

describe("Groups com conta restrita", () => {
  it("não oferece renomear o sistema, e diz por quê", async () => {
    render(<Groups scope={RESTRITA} />);

    await screen.findAllByText("RPA");
    const renomear = within(linha("RPA")).getByRole("button", { name: "Renomear" });
    expect(renomear).toBeDisabled();
    expect(renomear).toHaveAccessibleDescription(/acesso a todos os grupos/);

    // O ambiente do sistema concedido continua renomeável.
    expect(within(linha("prod")).getByRole("button", { name: "Renomear" })).toBeEnabled();
  });

  it("mostra os canais do grupo sem caixas de vínculo, com a explicação", async () => {
    render(<Groups scope={RESTRITA} />);

    await screen.findAllByText("prod");
    fireEvent.click(within(linha("prod")).getByRole("button", { name: "Avisos" }));

    expect(await screen.findByText("plantão")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByText(/só quem tem acesso a todos os grupos/i)).toBeInTheDocument();
    // A lista de canais da conta restrita vem recortada pelo servidor;
    // ela não serve de cardápio, e nem é pedida.
    expect(api.listChannels).not.toHaveBeenCalled();
  });
});

describe("Groups com escopo total", () => {
  it("mostra a recusa do servidor ao vincular canal", async () => {
    vi.mocked(api.groupChannels).mockResolvedValue({ items: [] });
    vi.mocked(api.linkGroupChannel).mockRejectedValue(
      new ApiError(403, "forbidden", "seu escopo não permite isso"),
    );

    render(<Groups scope={TOTAL} />);

    await screen.findAllByText("prod");
    fireEvent.click(within(linha("prod")).getByRole("button", { name: "Avisos" }));
    fireEvent.click(await screen.findByRole("checkbox", { name: /plantão/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("seu escopo não permite isso");
  });

  it("renomeia o sistema", async () => {
    render(<Groups scope={TOTAL} />);

    await screen.findAllByText("RPA");
    expect(within(linha("RPA")).getByRole("button", { name: "Renomear" })).toBeEnabled();
  });
});
