import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/types";
import { Gate } from "./Gate";

/**
 * A tela de entrada no modo de configuração inicial pede o token que o
 * servidor exige. Sem o campo, quem instalou não teria onde informá-lo; sem
 * a dica, não saberia onde procurá-lo.
 */

vi.mock("../api/client", () => ({
  api: { createAdmin: vi.fn(), login: vi.fn() },
}));

const { api } = await import("../api/client");

beforeEach(() => {
  vi.mocked(api.createAdmin).mockReset();
  vi.mocked(api.login).mockReset();
});

function preencher(token: string) {
  fireEvent.change(screen.getByLabelText(/usuário/), { target: { value: "admin" } });
  fireEvent.change(screen.getByLabelText(/^senha/), { target: { value: "uma-senha-bem-longa" } });
  fireEvent.change(screen.getByLabelText(/token de configuração/), { target: { value: token } });
}

describe("Gate", () => {
  it("pede o token de configuração e diz onde achá-lo", () => {
    render(<Gate mode="setup" onDone={() => {}} />);

    const campo = screen.getByLabelText(/token de configuração/);
    expect(campo).toBeRequired();
    expect(screen.getByText(/log do servidor/)).toBeInTheDocument();
    expect(screen.getByText(/UPWATCH_SETUP_TOKEN/)).toBeInTheDocument();
  });

  it("envia o token junto com a conta", async () => {
    vi.mocked(api.createAdmin).mockResolvedValue({} as never);
    vi.mocked(api.login).mockResolvedValue({ status: "ok" });
    const onDone = vi.fn();
    render(<Gate mode="setup" onDone={onDone} />);

    preencher("  token-de-configuracao-dos-testes  ");
    fireEvent.click(screen.getByRole("button", { name: /Criar conta/ }));

    // Os espaços em volta vêm da cópia do log, não do token.
    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(api.createAdmin).toHaveBeenCalledWith(
      "admin",
      "uma-senha-bem-longa",
      "token-de-configuracao-dos-testes",
    );
  });

  it("mostra a recusa do token no próprio campo", async () => {
    vi.mocked(api.createAdmin).mockRejectedValue(
      new ApiError(401, "unauthorized", "token de configuração inválido", "setup_token"),
    );
    render(<Gate mode="setup" onDone={() => {}} />);

    preencher("chute");
    fireEvent.click(screen.getByRole("button", { name: /Criar conta/ }));

    await waitFor(() =>
      expect(screen.getAllByText("token de configuração inválido").length).toBeGreaterThan(0),
    );
    expect(api.login).not.toHaveBeenCalled();
  });

  it("não pede token para entrar", () => {
    render(<Gate mode="login" onDone={() => {}} />);

    expect(screen.queryByLabelText(/token de configuração/)).not.toBeInTheDocument();
  });
});
