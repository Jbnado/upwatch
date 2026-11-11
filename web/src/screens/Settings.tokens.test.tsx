import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { Settings } from "./Settings";

/**
 * O formulário de criar token escolhe a validade e manda expires_at; o
 * seletor oferece só o que a política da instalação aceita. A regra está
 * testada em lib/token-expiry.test.ts; aqui prova-se a ligação com a API.
 */

const listTokens = vi.fn();
const createToken = vi.fn();
const revokeToken = vi.fn();
const updateTokenExpiry = vi.fn();
const rotateToken = vi.fn();

vi.mock("../api/client", () => ({
  api: {
    listTokens: () => listTokens(),
    createToken: (name: string, expiresAt?: string | null) => createToken(name, expiresAt),
    revokeToken: (id: number) => revokeToken(id),
    updateTokenExpiry: (id: number, expiresAt: string | null) => updateTokenExpiry(id, expiresAt),
    rotateToken: (id: number, expiresAt: string | null) => rotateToken(id, expiresAt),
  },
}));

vi.mock("./Groups", () => ({ Groups: () => null }));
vi.mock("./Users", () => ({ Users: () => null }));
vi.mock("./Channels", () => ({ Channels: () => null }));
vi.mock("./Maintenance", () => ({ Maintenance: () => null }));
vi.mock("./StatusPages", () => ({ StatusPages: () => null, StatusPageAdmin: () => null }));
vi.mock("./TwoFactor", () => ({ TwoFactor: () => null }));

function admin(): User {
  return {
    id: 1,
    username: "ana",
    role: "admin",
    scope: { all_groups: true, group_ids: [] },
    totp_enabled: false,
    created_at: "",
    updated_at: "",
  };
}

function abreFormulario() {
  render(<Settings eu={admin()} section="tokens" onSignedOut={() => {}} />);
  return screen.findByRole("button", { name: /Novo token/ }).then((botao) => {
    fireEvent.click(botao);
  });
}

beforeEach(() => {
  listTokens.mockReset();
  createToken.mockReset();
  revokeToken.mockReset();
  updateTokenExpiry.mockReset();
  rotateToken.mockReset();
  window.history.replaceState({}, "", "/ajustes/tokens");
});

describe("Tokens", () => {
  it("manda expires_at com o prazo padrão quando há validade a oferecer", async () => {
    listTokens.mockResolvedValue({
      items: [],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });
    createToken.mockResolvedValue({
      id: 1,
      name: "ci",
      prefix: "upw_abc",
      created_at: "",
      token: "upw_segredo",
    });

    await abreFormulario();
    fireEvent.change(screen.getByPlaceholderText("pipeline de deploy"), {
      target: { value: "ci" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Criar token" }));

    await waitFor(() => expect(createToken).toHaveBeenCalled());
    const [nome, expiresAt] = createToken.mock.calls[0]!;
    expect(nome).toBe("ci");
    // O padrão é um prazo finito (30 dias), então vem uma data, não null.
    expect(expiresAt).toBeTruthy();
  });

  it("não oferece 'sem expiração' quando a instalação exige validade", async () => {
    listTokens.mockResolvedValue({
      items: [],
      policy: { require_expiry: true, max_lifetime_seconds: 0 },
    });

    await abreFormulario();
    const seletor = screen.getByRole("combobox", { name: /validade/i });
    expect(within(seletor).queryByRole("option", { name: "sem expiração" })).toBeNull();
  });

  it("deixa escolher 'sem expiração' e então manda expires_at nulo", async () => {
    listTokens.mockResolvedValue({
      items: [],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });
    createToken.mockResolvedValue({
      id: 2,
      name: "eterno",
      prefix: "upw_xyz",
      created_at: "",
      token: "upw_segredo",
    });

    await abreFormulario();
    fireEvent.change(screen.getByPlaceholderText("pipeline de deploy"), {
      target: { value: "eterno" },
    });
    fireEvent.change(screen.getByRole("combobox", { name: /validade/i }), {
      target: { value: "none" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Criar token" }));

    await waitFor(() => expect(createToken).toHaveBeenCalled());
    const [, expiresAt] = createToken.mock.calls[0]!;
    expect(expiresAt ?? null).toBeNull();
  });

  it("edita a validade de um token existente", async () => {
    listTokens.mockResolvedValue({
      items: [{ id: 7, name: "ci", prefix: "upw_abc", created_at: "2026-01-01T00:00:00Z" }],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });
    updateTokenExpiry.mockResolvedValue(undefined);

    render(<Settings eu={admin()} section="tokens" onSignedOut={() => {}} />);
    await waitFor(() => expect(screen.getByText("ci")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: /editar validade/i }));
    fireEvent.change(screen.getByRole("combobox", { name: /validade/i }), {
      target: { value: "30d" },
    });
    fireEvent.click(screen.getByRole("button", { name: /salvar/i }));

    await waitFor(() => expect(updateTokenExpiry).toHaveBeenCalled());
    const [id, expiresAt] = updateTokenExpiry.mock.calls[0]!;
    expect(id).toBe(7);
    expect(expiresAt).toBeTruthy();
  });

  it("rotaciona um token e mostra o segredo novo", async () => {
    listTokens.mockResolvedValue({
      items: [{ id: 5, name: "ci", prefix: "upw_abc", created_at: "2026-01-01T00:00:00Z" }],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });
    rotateToken.mockResolvedValue({
      id: 6,
      name: "ci",
      prefix: "upw_def",
      created_at: "",
      token: "upw_segredo_novo",
    });

    render(<Settings eu={admin()} section="tokens" onSignedOut={() => {}} />);
    await waitFor(() => expect(screen.getByText("ci")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: /rotacionar/i }));

    await waitFor(() => expect(rotateToken).toHaveBeenCalled());
    expect(rotateToken.mock.calls[0]![0]).toBe(5);
    expect(await screen.findByText("upw_segredo_novo")).toBeInTheDocument();
  });

  it("marca o token rotacionado e não oferece rotacioná-lo de novo", async () => {
    listTokens.mockResolvedValue({
      items: [
        {
          id: 9,
          name: "ci",
          prefix: "upw_abc",
          created_at: "2026-01-01T00:00:00Z",
          rotated_at: "2026-02-01T00:00:00Z",
        },
      ],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });

    render(<Settings eu={admin()} section="tokens" onSignedOut={() => {}} />);
    await waitFor(() => expect(screen.getByText("ci")).toBeInTheDocument());

    expect(screen.getByText(/rotacionado/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /rotacionar/i })).toBeNull();
  });

  it("mostra a expiração na listagem", async () => {
    listTokens.mockResolvedValue({
      items: [
        {
          id: 3,
          name: "ci",
          prefix: "upw_abc",
          created_at: "2026-01-01T00:00:00Z",
          expires_at: "2026-06-01T00:00:00Z",
        },
      ],
      policy: { require_expiry: false, max_lifetime_seconds: 0 },
    });

    render(<Settings eu={admin()} section="tokens" onSignedOut={() => {}} />);
    await waitFor(() => expect(screen.getByText("ci")).toBeInTheDocument());
    expect(screen.getByText(/expira/i)).toBeInTheDocument();
  });
});
