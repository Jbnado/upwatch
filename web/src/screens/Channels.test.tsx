import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Channel, ChannelDetail } from "../api/types";
import { ChannelForm, Channels } from "./Channels";

/**
 * A edição de um canal pela tela.
 *
 * Editar é atualizar no lugar: remover e criar de novo desfaria os
 * vínculos com grupos e monitores. E os segredos não voltam do servidor,
 * então o campo em branco precisa significar "manter", e não "apagar".
 */

vi.mock("../api/client", () => ({
  api: {
    listChannels: vi.fn(),
    getChannel: vi.fn(),
    createChannel: vi.fn(),
    updateChannel: vi.fn(),
    deleteChannel: vi.fn(),
    testChannel: vi.fn(),
    unlinkChannel: vi.fn(),
    unlinkGroupChannel: vi.fn(),
  },
}));

const { api } = await import("../api/client");

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.updateChannel).mockResolvedValue({} as never);
});

const base = { enabled: true, created_at: "", updated_at: "" };

const EMAIL: ChannelDetail = {
  ...base,
  id: 7,
  name: "e-mail do plantão",
  type: "email",
  enabled: false,
  config: {
    host: "smtp.exemplo.com",
    port: 465,
    security: "tls",
    username: "alertas",
    password_set: true,
    from: "alertas@exemplo.com",
    to: ["plantao@exemplo.com", "infra@exemplo.com"],
  },
};

const WEBHOOK: ChannelDetail = {
  ...base,
  id: 9,
  name: "integração",
  type: "webhook",
  config: {
    url_set: true,
    headers: { Authorization: "***" },
    template: "{{.Monitor.Name}} mudou",
    body_template: { event: "$status" },
  },
};

function editar(canal: ChannelDetail) {
  const onSaved = vi.fn().mockResolvedValue(undefined);
  render(<ChannelForm canal={canal} onSaved={onSaved} onError={() => {}} onCancel={() => {}} />);
  return onSaved;
}

function salvar() {
  fireEvent.click(screen.getByRole("button", { name: "Salvar canal" }));
}

describe("ChannelForm em edição", () => {
  it("abre com os valores atuais e a senha em branco", () => {
    editar(EMAIL);

    expect(screen.getByLabelText(/^nome/)).toHaveValue("e-mail do plantão");
    expect(screen.getByLabelText(/servidor SMTP/)).toHaveValue("smtp.exemplo.com");
    expect(screen.getByLabelText(/^porta/)).toHaveValue("465");
    expect(screen.getByLabelText(/destinatários/)).toHaveValue(
      "plantao@exemplo.com, infra@exemplo.com",
    );
    const senha = screen.getByLabelText(/^senha/);
    expect(senha).toHaveValue("");
    expect(senha).toHaveAttribute("placeholder", "mantida; preencha para trocar");
    // O tipo não muda na edição: nada do que está guardado serviria.
    expect(screen.getByLabelText(/onde avisar/)).toBeDisabled();
  });

  it("salva no mesmo canal, sem a senha em branco e sem religá-lo", async () => {
    const onSaved = editar(EMAIL);

    fireEvent.change(screen.getByLabelText(/destinatários/), {
      target: { value: "plantao@exemplo.com" },
    });
    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(api.updateChannel).toHaveBeenCalledWith(7, {
      name: "e-mail do plantão",
      type: "email",
      enabled: false,
      config: {
        host: "smtp.exemplo.com",
        port: 465,
        security: "tls",
        username: "alertas",
        from: "alertas@exemplo.com",
        to: ["plantao@exemplo.com"],
      },
    });
    expect(api.createChannel).not.toHaveBeenCalled();
    expect(api.deleteChannel).not.toHaveBeenCalled();
  });

  it("manda a senha nova quando preenchida", async () => {
    const onSaved = editar(EMAIL);

    fireEvent.change(screen.getByLabelText(/^senha/), { target: { value: "nova" } });
    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.updateChannel).mock.calls[0]![1].config).toMatchObject({
      password: "nova",
    });
  });

  it("mostra os cabeçalhos guardados pelo nome, com o valor oculto", () => {
    editar(WEBHOOK);

    expect(screen.getByLabelText("nome do cabeçalho 1")).toHaveValue("Authorization");
    const valor = screen.getByLabelText("valor do cabeçalho 1");
    expect(valor).toHaveValue("");
    expect(valor).toHaveAttribute("placeholder", "mantido; preencha para trocar");

    const url = screen.getByLabelText(/endereço do webhook/);
    expect(url).toHaveAttribute("placeholder", "mantida; preencha para trocar");
    expect(url).not.toBeRequired();
  });

  it("em branco, mantém a URL e o valor dos cabeçalhos guardados", async () => {
    const onSaved = editar(WEBHOOK);

    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(api.updateChannel).toHaveBeenCalledWith(9, {
      name: "integração",
      type: "webhook",
      enabled: true,
      config: {
        headers: { Authorization: "***" },
        template: "{{.Monitor.Name}} mudou",
        body_template: { event: "$status" },
      },
    });
  });

  it("troca o valor do cabeçalho quando preenchido", async () => {
    const onSaved = editar(WEBHOOK);

    fireEvent.change(screen.getByLabelText("valor do cabeçalho 1"), {
      target: { value: "Bearer novo" },
    });
    fireEvent.change(screen.getByLabelText(/endereço do webhook/), {
      target: { value: "https://outro.exemplo/alertas" },
    });
    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.updateChannel).mock.calls[0]![1].config).toMatchObject({
      url: "https://outro.exemplo/alertas",
      headers: { Authorization: "Bearer novo" },
    });
  });

  it("recolher o bloco avançado não salva o canal", () => {
    editar(WEBHOOK);

    fireEvent.click(screen.getByRole("button", { name: /cabeçalhos e formato do corpo/ }));

    expect(api.updateChannel).not.toHaveBeenCalled();
  });
});

describe("Channels", () => {
  const LISTA: Channel[] = [{ ...base, id: 9, name: "integração", type: "webhook" }];

  it("edita pelo botão da lista e recarrega, sem remover o canal", async () => {
    vi.mocked(api.listChannels).mockResolvedValue({ items: LISTA });
    vi.mocked(api.getChannel).mockResolvedValue(WEBHOOK);
    render(<Channels />);

    fireEvent.click(await screen.findByRole("button", { name: "Editar" }));
    expect(await screen.findByDisplayValue("integração")).toBeInTheDocument();
    expect(api.getChannel).toHaveBeenCalledWith(9);

    salvar();

    await waitFor(() => expect(api.listChannels).toHaveBeenCalledTimes(2));
    expect(api.updateChannel).toHaveBeenCalledWith(9, expect.anything());
    expect(screen.queryByRole("button", { name: "Salvar canal" })).not.toBeInTheDocument();
    // Os vínculos ficam com o canal: nada foi removido nem desvinculado.
    expect(api.deleteChannel).not.toHaveBeenCalled();
    expect(api.unlinkChannel).not.toHaveBeenCalled();
    expect(api.unlinkGroupChannel).not.toHaveBeenCalled();
  });
});
