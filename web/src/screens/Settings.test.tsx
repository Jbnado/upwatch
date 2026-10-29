import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { Settings } from "./Settings";

/**
 * Os ajustes com menu lateral, uma seção por vez.
 *
 * O menu é a resposta a "o que posso mexer aqui?": oferece só o que o
 * servidor aceita da conta, marca onde a pessoa está e, para quem chega
 * por um link a uma seção que não é dela, explica por quê em vez de
 * mostrar uma tela vazia.
 */

// As seções têm testes próprios; aqui interessa o menu e a moldura.
vi.mock("./Groups", () => ({ Groups: () => <p>tela de grupos</p> }));
vi.mock("./Users", () => ({ Users: () => <p>tela de contas</p> }));
vi.mock("./Channels", () => ({ Channels: () => <p>tela de canais</p> }));
vi.mock("./StatusPages", () => ({
  StatusPages: () => <p>tela de páginas</p>,
  StatusPageAdmin: ({ id }: { id: number }) => <p>página {id}</p>,
}));
vi.mock("../api/client", () => ({
  api: { listTokens: vi.fn().mockResolvedValue({ items: [] }), changePassword: vi.fn() },
}));

function conta(role: User["role"], all_groups: boolean): User {
  return {
    id: 1,
    username: "ana",
    role,
    scope: { all_groups, group_ids: all_groups ? [] : [4] },
    created_at: "",
    updated_at: "",
  };
}

const menu = () => screen.getByRole("navigation", { name: "Seções dos ajustes" });
// O "›" do celular é decoração (aria-hidden) e não faz parte do nome.
const itens = () =>
  within(menu())
    .getAllByRole("link")
    .map((a) => a.textContent?.replace("›", "").trim());

beforeEach(() => {
  window.history.replaceState({}, "", "/ajustes");
});

describe("Settings", () => {
  it("mostra o menu inteiro a quem administra tudo", () => {
    render(<Settings eu={conta("admin", true)} section="grupos" onSignedOut={() => {}} />);

    expect(itens()).toEqual([
      "Grupos",
      "Contas e acesso",
      "Canais",
      "Manutenção",
      "Páginas de estado",
      "Tokens de API",
      "Senha",
    ]);
  });

  it("reduz o menu da conta restrita", () => {
    render(<Settings eu={conta("admin", false)} section="grupos" onSignedOut={() => {}} />);

    expect(itens()).toEqual(["Grupos", "Contas e acesso", "Tokens de API", "Senha"]);
  });

  it("deixa ao observador só a senha", () => {
    render(<Settings eu={conta("viewer", true)} onSignedOut={() => {}} />);

    expect(itens()).toEqual(["Senha"]);
    expect(screen.getByRole("heading", { name: "Senha" })).toBeInTheDocument();
  });

  it("marca a seção aberta com aria-current", () => {
    render(<Settings eu={conta("admin", true)} section="canais" onSignedOut={() => {}} />);

    expect(within(menu()).getByRole("link", { name: "Canais" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(within(menu()).getByRole("link", { name: "Grupos" })).not.toHaveAttribute(
      "aria-current",
    );
    expect(screen.getByText("tela de canais")).toBeInTheDocument();
  });

  it("abre a primeira seção da conta quando nenhuma foi escolhida", () => {
    render(<Settings eu={conta("admin", true)} onSignedOut={() => {}} />);

    expect(screen.getByText("tela de grupos")).toBeInTheDocument();
    expect(within(menu()).getByRole("link", { name: "Grupos" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("explica a recusa a quem chega por link numa seção que não é dele", () => {
    render(<Settings eu={conta("admin", false)} section="paginas" onSignedOut={() => {}} />);

    expect(screen.queryByText("tela de páginas")).not.toBeInTheDocument();
    expect(screen.getByText(/só quem tem acesso a todos os grupos/)).toBeInTheDocument();
  });

  it("guarda a edição da página de estado dentro da seção de páginas", () => {
    render(<Settings eu={conta("admin", true)} section="paginas" pageId={4} onSignedOut={() => {}} />);

    expect(screen.getByText("página 4")).toBeInTheDocument();
    expect(within(menu()).getByRole("link", { name: "Páginas de estado" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("navega pelo menu sem recarregar a aplicação", () => {
    render(<Settings eu={conta("admin", true)} section="grupos" onSignedOut={() => {}} />);

    fireEvent.click(within(menu()).getByRole("link", { name: "Tokens de API" }));

    expect(window.location.pathname).toBe("/ajustes/tokens");
  });

  it("oferece a volta ao menu dentro de uma seção, para o celular", () => {
    // No celular o menu e a seção não cabem lado a lado: a seção ocupa a
    // tela, e a volta precisa estar à vista.
    render(<Settings eu={conta("admin", true)} section="canais" onSignedOut={() => {}} />);

    expect(screen.getByRole("link", { name: "← Ajustes" })).toHaveAttribute("href", "/ajustes");
  });
});
