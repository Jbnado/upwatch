import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Group, Monitor, StatusPage, StatusPageComponent } from "../api/types";
import { StatusPageAdmin, StatusPages } from "./StatusPages";

/**
 * A edição da página pública, no caso que motivou o seletor em árvore:
 * "erp" existe em Retaguarda · prod e em Retaguarda · hml, e a tela
 * precisa deixar claro qual dos dois está publicado.
 */

vi.mock("../api/client", () => ({
  api: {
    listStatusPages: vi.fn(),
    getStatusPage: vi.fn(),
    listMonitors: vi.fn(),
    listGroups: vi.fn(),
    setComponent: vi.fn(),
    removeComponent: vi.fn(),
    announcements: vi.fn(),
    createStatusPage: vi.fn(),
  },
}));

const { api } = await import("../api/client");

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

const PAGINA: StatusPage = {
  id: 1,
  slug: "avenida",
  title: "Estado dos sistemas Avenida",
  show_latency: false,
  enabled: true,
  default: false,
  created_at: "",
  updated_at: "",
};

const GRUPOS = [grupo(4, "Retaguarda"), grupo(5, "prod", 4, "Retaguarda"), grupo(6, "hml", 4, "Retaguarda")];
const MONITORES = [monitor(21, "erp", 5), monitor(23, "erp", 6), monitor(20, "fiscal", 5)];

function comComponentes(components: StatusPageComponent[]) {
  vi.mocked(api.getStatusPage).mockResolvedValue({
    page: PAGINA,
    groups: [{ id: 9, page_id: 1, name: "Retaguarda", position: 1 }],
    components,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.listMonitors).mockResolvedValue({ items: MONITORES, has_more: false } as never);
  vi.mocked(api.listGroups).mockResolvedValue({ items: GRUPOS });
  vi.mocked(api.announcements).mockResolvedValue({ items: [], has_more: false } as never);
  vi.mocked(api.setComponent).mockResolvedValue({} as never);
});

describe("StatusPageAdmin", () => {
  it("mostra de qual grupo vem cada serviço publicado", async () => {
    comComponentes([{ page_id: 1, monitor_id: 21, group_id: 9, label: "ERP", position: 1 }]);
    render(<StatusPageAdmin id={1} />);

    const na = await screen.findByText("na página");
    const bloco = na.parentElement!;
    expect(within(bloco).getByText("ERP")).toBeInTheDocument();
    expect(within(bloco).getByText(/Retaguarda · prod/)).toBeInTheDocument();
  });

  it("chama de Seções as divisões da página, não de grupos", async () => {
    // "Grupo" já é o Sistema › Ambiente dos monitores.
    comComponentes([]);
    render(<StatusPageAdmin id={1} />);

    expect(await screen.findByRole("heading", { name: "Seções" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Grupos" })).not.toBeInTheDocument();
  });

  it("publica o erp de produção, e não o de homologação", async () => {
    comComponentes([]);
    render(<StatusPageAdmin id={1} />);

    fireEvent.click(await screen.findByRole("button", { name: /Abrir prod/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /^erp.*Retaguarda · prod/ }));

    await waitFor(() => expect(api.setComponent).toHaveBeenCalledWith(1, 21, { label: "" }));
    expect(api.setComponent).not.toHaveBeenCalledWith(1, 23, expect.anything());
  });

  it("deixa o rótulo público e a seção sob o serviço marcado", async () => {
    comComponentes([{ page_id: 1, monitor_id: 21, group_id: null, label: "", position: 1 }]);
    render(<StatusPageAdmin id={1} />);

    const rotulo = await screen.findByPlaceholderText("erp");
    fireEvent.change(rotulo, { target: { value: "ERP" } });
    fireEvent.blur(rotulo);

    await waitFor(() =>
      expect(api.setComponent).toHaveBeenCalledWith(1, 21, { label: "ERP", group_id: null }),
    );
    expect(screen.getByRole("combobox", { name: "seção" })).toBeInTheDocument();
  });
});

describe("StatusPages", () => {
  it("mostra a lista e guarda o formulário atrás de + Nova página", async () => {
    vi.mocked(api.listStatusPages).mockResolvedValue({ items: [PAGINA] });
    render(<StatusPages />);

    expect(await screen.findByText("Estado dos sistemas Avenida")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /^título/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "+ Nova página" }));
    expect(screen.getByRole("textbox", { name: /^título/ })).toHaveFocus();

    fireEvent.click(screen.getByRole("button", { name: "Cancelar" }));
    expect(screen.queryByRole("textbox", { name: /^título/ })).not.toBeInTheDocument();
  });
});
