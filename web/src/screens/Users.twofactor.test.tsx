import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { Users } from "./Users";

/**
 * O segundo fator na lista de contas: quem tem, e o desligamento para
 * quem perdeu o celular. A própria conta não ganha o botão — desligar a
 * sua pede senha e código, na seção da conta.
 */

vi.mock("../api/client", () => ({
  api: {
    listUsers: vi.fn(),
    listGroups: vi.fn(),
    resetUserTOTP: vi.fn(),
  },
}));

const { api } = await import("../api/client");

function conta(id: number, username: string, totp_enabled: boolean): User {
  return {
    id,
    username,
    role: "admin",
    scope: { all_groups: true, group_ids: [] },
    totp_enabled,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

const eu = conta(1, "ana", true);
const bia = conta(2, "bia", true);
const caio = conta(3, "caio", false);

beforeEach(() => {
  vi.mocked(api.listUsers).mockReset().mockResolvedValue({ items: [eu, bia, caio] });
  vi.mocked(api.listGroups).mockReset().mockResolvedValue({ items: [] } as never);
  vi.mocked(api.resetUserTOTP).mockReset().mockResolvedValue(undefined);
});

const linha = async (nome: string) => (await screen.findByText(nome)).closest("li")!;

describe("Users, verificação em duas etapas", () => {
  it("mostra quem tem o segundo fator", async () => {
    render(<Users eu={eu} />);

    expect(within(await linha("bia")).getByText("2 etapas")).toBeInTheDocument();
    expect(within(await linha("caio")).queryByText("2 etapas")).not.toBeInTheDocument();
  });

  it("oferece desligar só nas outras contas que têm o fator", async () => {
    render(<Users eu={eu} />);

    await linha("bia");
    expect(screen.getAllByRole("button", { name: /desligar a verificação/ })).toHaveLength(1);
    expect(
      screen.getByRole("button", { name: "desligar a verificação em duas etapas de bia" }),
    ).toBeInTheDocument();
  });

  it("desliga depois de confirmar", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<Users eu={eu} />);

    fireEvent.click(
      await screen.findByRole("button", { name: "desligar a verificação em duas etapas de bia" }),
    );

    await waitFor(() => expect(api.resetUserTOTP).toHaveBeenCalledWith(2));
    expect(api.listUsers).toHaveBeenCalledTimes(2);
  });
});
