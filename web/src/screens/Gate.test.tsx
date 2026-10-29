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
  api: { createAdmin: vi.fn(), login: vi.fn(), loginSecondFactor: vi.fn() },
}));

const { api } = await import("../api/client");

beforeEach(() => {
  vi.mocked(api.createAdmin).mockReset();
  vi.mocked(api.login).mockReset();
  vi.mocked(api.loginSecondFactor).mockReset();
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

/**
 * O segundo passo do login, para conta com verificação em duas etapas.
 *
 * A senha certa não termina o login: a tela pede o código e só então
 * avisa que entrou. Sem o celular, o mesmo campo aceita um código de
 * recuperação; e um desafio vencido devolve à senha, porque sem ele o
 * código não serve para nada.
 */
describe("Gate, segundo passo", () => {
  function entrar() {
    fireEvent.change(screen.getByLabelText(/usuário/), { target: { value: "admin" } });
    fireEvent.change(screen.getByLabelText(/^senha/), { target: { value: "uma-senha-bem-longa" } });
    fireEvent.click(screen.getByRole("button", { name: "Entrar" }));
  }

  beforeEach(() => {
    vi.mocked(api.login).mockResolvedValue({
      status: "two_factor_required",
      challenge: "desafio-1",
      expires_at: "",
    });
  });

  it("pede o código depois da senha e só então entra", async () => {
    vi.mocked(api.loginSecondFactor).mockResolvedValue({ status: "ok" });
    const onDone = vi.fn();
    render(<Gate mode="login" onDone={onDone} />);

    entrar();
    const campo = await screen.findByLabelText(/^código$/);
    expect(onDone).not.toHaveBeenCalled();
    expect(campo).toHaveAttribute("inputmode", "numeric");
    expect(campo).toHaveAttribute("autocomplete", "one-time-code");

    fireEvent.change(campo, { target: { value: " 123456 " } });
    fireEvent.click(screen.getByRole("button", { name: "Verificar" }));

    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(api.loginSecondFactor).toHaveBeenCalledWith("desafio-1", "123456");
  });

  it("aceita código de recuperação no mesmo campo", async () => {
    vi.mocked(api.loginSecondFactor).mockResolvedValue({ status: "ok" });
    const onDone = vi.fn();
    render(<Gate mode="login" onDone={onDone} />);

    entrar();
    fireEvent.click(await screen.findByRole("button", { name: "usar código de recuperação" }));
    const campo = screen.getByLabelText(/código de recuperação/);
    expect(campo).toHaveAttribute("inputmode", "text");

    fireEvent.change(campo, { target: { value: "abcd-efgh-ijkm-npqr" } });
    fireEvent.click(screen.getByRole("button", { name: "Verificar" }));

    await waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(api.loginSecondFactor).toHaveBeenCalledWith("desafio-1", "abcd-efgh-ijkm-npqr");
  });

  it("mostra o código recusado e deixa tentar de novo", async () => {
    vi.mocked(api.loginSecondFactor).mockRejectedValue(
      new ApiError(401, "unauthorized", "código inválido", "code"),
    );
    render(<Gate mode="login" onDone={() => {}} />);

    entrar();
    fireEvent.change(await screen.findByLabelText(/^código$/), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "Verificar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("código inválido");
    expect(screen.getByLabelText(/^código$/)).toHaveValue("");
  });

  it("volta para a senha quando o desafio expira", async () => {
    vi.mocked(api.loginSecondFactor).mockRejectedValue(
      new ApiError(401, "unauthorized", "a verificação expirou; entre de novo", "challenge"),
    );
    render(<Gate mode="login" onDone={() => {}} />);

    entrar();
    fireEvent.change(await screen.findByLabelText(/^código$/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Verificar" }));

    expect(await screen.findByRole("button", { name: "Entrar" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("a verificação expirou");
    expect(screen.getByLabelText(/^senha/)).toHaveValue("");
  });
});
