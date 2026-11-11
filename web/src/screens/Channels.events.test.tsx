import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChannelDetail } from "../api/types";
import { ChannelForm } from "./Channels";

/**
 * O filtro de eventos por canal.
 *
 * Por padrão o canal recebe tudo (não manda nada e o servidor mantém o
 * comportamento de sempre). Quem filtra escolhe, mas a queda é sempre
 * avisada — esconder a queda é pior que não avisar — e a tela diz o que
 * ficou silenciado.
 */

vi.mock("../api/client", () => ({
  api: {
    createChannel: vi.fn(),
    updateChannel: vi.fn(),
    listGroups: vi.fn(),
    listMonitors: vi.fn(),
  },
}));

const { api } = await import("../api/client");

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.createChannel).mockResolvedValue({} as never);
  vi.mocked(api.updateChannel).mockResolvedValue({} as never);
  vi.mocked(api.listGroups).mockResolvedValue({ items: [] } as never);
  vi.mocked(api.listMonitors).mockResolvedValue({ items: [] } as never);
});

function criar() {
  const onSaved = vi.fn().mockResolvedValue(undefined);
  render(<ChannelForm onSaved={onSaved} onError={() => {}} onCancel={() => {}} />);
  fireEvent.change(screen.getByLabelText(/^nome/), { target: { value: "plantão" } });
  fireEvent.change(screen.getByLabelText(/endereço do webhook/), {
    target: { value: "https://exemplo.com/hook" },
  });
  return onSaved;
}

describe("ChannelForm, quais eventos", () => {
  it("por padrão não manda eventos — recebe tudo", async () => {
    const onSaved = criar();

    fireEvent.click(screen.getByRole("button", { name: "Adicionar canal" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.createChannel).mock.calls[0]![0]).not.toHaveProperty("events");
  });

  it("filtrar e silenciar as voltas manda queda e degradação, nunca a volta", async () => {
    const onSaved = criar();

    fireEvent.click(screen.getByRole("checkbox", { name: /só alguns eventos/ }));
    // A queda é sempre avisada e não há como desmarcá-la.
    expect(screen.getByText(/sempre avisado/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /Quedas/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: /Voltas ao ar/ }));
    // A tela diz o que ficou silenciado.
    expect(screen.getByText(/Silenciado/)).toHaveTextContent(/[Vv]oltas/);

    fireEvent.click(screen.getByRole("button", { name: "Adicionar canal" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const events = vi.mocked(api.createChannel).mock.calls[0]![0].events;
    expect(events).toEqual(expect.arrayContaining(["monitor.down", "monitor.degraded"]));
    expect(events).not.toContain("monitor.up");
  });

  it("na edição abre com o filtro atual e mostra o que está silenciado", () => {
    const canal: ChannelDetail = {
      id: 9,
      name: "integração",
      type: "webhook",
      enabled: true,
      created_at: "",
      updated_at: "",
      events: ["monitor.down"],
      config: { url_set: true },
    };
    render(<ChannelForm canal={canal} onSaved={vi.fn()} onError={() => {}} onCancel={() => {}} />);

    expect(screen.getByRole("checkbox", { name: /só alguns eventos/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Degradações/ })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: /Voltas ao ar/ })).not.toBeChecked();
    expect(screen.getByText(/Silenciado/)).toBeInTheDocument();
  });
});
