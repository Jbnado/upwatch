import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Channel, ChannelDetail, Group, Monitor } from "../api/types";
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
    listGroups: vi.fn(),
    listMonitors: vi.fn(),
  },
}));

const { api } = await import("../api/client");

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.updateChannel).mockResolvedValue({} as never);
  vi.mocked(api.createChannel).mockResolvedValue({} as never);
  vi.mocked(api.listGroups).mockResolvedValue({ items: GRUPOS });
  vi.mocked(api.listMonitors).mockResolvedValue({ items: MONITORES } as never);
});

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return { id, name, slug: name, parent_id, path: pai ? `${pai} / ${name}` : name, created_at: "", updated_at: "" };
}

function monitor(id: number, name: string, group_id: number | null): Monitor {
  return {
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
  };
}

// RPA com prod e hml; hml e o sistema Vazio ainda sem monitor.
const GRUPOS = [grupo(1, "RPA"), grupo(2, "prod", 1, "RPA"), grupo(3, "hml", 1, "RPA"), grupo(4, "Vazio")];
const MONITORES = [monitor(10, "api", 2), monitor(11, "erp", 2), monitor(20, "solto", null)];

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

const caixa = async (nome: RegExp) => screen.findByRole("checkbox", { name: nome });

describe("ChannelForm, onde avisar", () => {
  function criar() {
    const onSaved = vi.fn().mockResolvedValue(undefined);
    render(<ChannelForm onSaved={onSaved} onError={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText(/^nome/), { target: { value: "plantão" } });
    fireEvent.change(screen.getByLabelText(/endereço do webhook/), {
      target: { value: "https://exemplo.com/hook" },
    });
    return onSaved;
  }

  it("cria o canal já ligado ao grupo marcado e ao grupo vazio", async () => {
    const onSaved = criar();

    fireEvent.click(await caixa(/^RPA/));
    fireEvent.click(await caixa(/^Vazio/));
    fireEvent.click(screen.getByRole("button", { name: /^Abrir Sem grupo/ }));
    fireEvent.click(await caixa(/^solto/));
    fireEvent.click(screen.getByRole("button", { name: "Adicionar canal" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.createChannel).mock.calls[0]![0].targets).toEqual({
      group_ids: [1, 4],
      monitor_ids: [20],
    });
  });

  it("parte do ambiente vira vínculo com cada monitor, e o pai fica parcial", async () => {
    const onSaved = criar();

    fireEvent.click(await caixa(/^RPA/));
    fireEvent.click(screen.getByRole("button", { name: /^Abrir prod/ }));
    fireEvent.click(await caixa(/^erp/));
    expect(await caixa(/^RPA/)).toBePartiallyChecked();
    expect(await caixa(/^prod/)).toBePartiallyChecked();
    fireEvent.click(screen.getByRole("button", { name: "Adicionar canal" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.createChannel).mock.calls[0]![0].targets).toEqual({
      group_ids: [3],
      monitor_ids: [10],
    });
  });

  it("na edição, abre com os vínculos atuais e não os manda sem mexer", async () => {
    const onSaved = editar({ ...WEBHOOK, targets: { group_ids: [2], monitor_ids: [20] } });

    expect(await caixa(/^api/)).toBeChecked();
    expect(await caixa(/^solto/)).toBeChecked();
    expect(await caixa(/^RPA/)).toBePartiallyChecked();
    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(vi.mocked(api.updateChannel).mock.calls[0]![1]).not.toHaveProperty("targets");
  });

  it("na edição, desmarcar troca o conjunto inteiro", async () => {
    const onSaved = editar({ ...WEBHOOK, targets: { group_ids: [2], monitor_ids: [20, 99] } });

    fireEvent.click(await caixa(/^solto/));
    salvar();

    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    // O 99 não está na árvore: volta junto, porque ninguém o desmarcou.
    expect(vi.mocked(api.updateChannel).mock.calls[0]![1].targets).toEqual({
      group_ids: [2],
      monitor_ids: [99],
    });
  });
});

describe("Channels, resumo de onde avisa", () => {
  it("diz em quantos grupos e monitores cada canal está ligado", async () => {
    vi.mocked(api.listChannels).mockResolvedValue({
      items: [
        { ...base, id: 1, name: "plantão", type: "webhook", targets: { group_ids: [1, 2, 3, 4, 5], monitor_ids: [7, 8] } },
        { ...base, id: 2, name: "solto", type: "slack", targets: { group_ids: [], monitor_ids: [9] } },
        { ...base, id: 3, name: "novo", type: "discord", targets: { group_ids: [], monitor_ids: [] } },
      ],
    });
    render(<Channels />);

    expect(await screen.findByText("5 grupos, 2 monitores")).toBeInTheDocument();
    expect(screen.getByText("1 monitor")).toBeInTheDocument();
    expect(screen.getByText("não está ligado a nada")).toBeInTheDocument();
  });
});
