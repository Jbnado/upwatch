import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, type TOTPStatus } from "../api/types";
import { TwoFactor } from "./TwoFactor";

/**
 * O cadastro da verificação em duas etapas, do começo aos códigos de
 * recuperação.
 *
 * O que importa aqui é a ordem: o QR code e a chave aparecem antes de
 * qualquer coisa ligar, o código do aplicativo confirma, os códigos de
 * recuperação aparecem uma vez, e só depois de a pessoa dizer que os
 * guardou o resto da interface fica sabendo.
 */

vi.mock("../api/client", () => ({
  api: {
    totpStatus: vi.fn(),
    totpSetup: vi.fn(),
    totpEnable: vi.fn(),
    totpDisable: vi.fn(),
    totpRecoveryCodes: vi.fn(),
  },
}));

const { api } = await import("../api/client");

const desligada: TOTPStatus = { enabled: false, pending: false, recovery_codes_left: 0, required: false };
const ligada: TOTPStatus = { enabled: true, pending: false, recovery_codes_left: 10, required: false };
const CODIGOS = Array.from({ length: 10 }, (_, i) => `aaaa-bbbb-cccc-dd${String(i).padStart(2, "0")}`);

beforeEach(() => {
  for (const f of Object.values(api)) vi.mocked(f).mockReset();
});

describe("TwoFactor, cadastro", () => {
  it("mostra QR code e chave, confirma com o código e mostra os códigos uma vez", async () => {
    vi.mocked(api.totpStatus).mockResolvedValueOnce(desligada).mockResolvedValue(ligada);
    vi.mocked(api.totpSetup).mockResolvedValue({
      secret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
      uri: "otpauth://totp/UpWatch:admin?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=UpWatch",
    });
    vi.mocked(api.totpEnable).mockResolvedValue({ recovery_codes: CODIGOS });
    const onChanged = vi.fn();
    render(<TwoFactor onChanged={onChanged} />);

    fireEvent.click(await screen.findByRole("button", { name: "Ligar verificação em duas etapas" }));

    expect(await screen.findByRole("img", { name: /QR code/ })).toBeInTheDocument();
    // A chave em blocos de quatro, como os aplicativos a mostram.
    expect(screen.getByText("JBSW Y3DP EHPK 3PXP JBSW Y3DP EHPK 3PXP")).toBeInTheDocument();
    expect(api.totpEnable).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText(/código que o aplicativo mostra/), {
      target: { value: "123456" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirmar e ligar" }));

    const lista = await screen.findByRole("list", { name: "códigos de recuperação" });
    expect(lista.querySelectorAll("li")).toHaveLength(10);
    expect(screen.getByText(/não aparecem de novo/)).toBeInTheDocument();
    expect(api.totpEnable).toHaveBeenCalledWith("123456");
    // Avisar agora faria a tela obrigatória sumir com os códigos.
    expect(onChanged).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "já guardei" }));

    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(await screen.findByText(/Restam 10 códigos/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "códigos de recuperação" })).not.toBeInTheDocument();
  });

  it("copia os códigos", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    vi.mocked(api.totpStatus).mockResolvedValue(desligada);
    vi.mocked(api.totpSetup).mockResolvedValue({ secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/x" });
    vi.mocked(api.totpEnable).mockResolvedValue({ recovery_codes: CODIGOS });
    render(<TwoFactor />);

    fireEvent.click(await screen.findByRole("button", { name: "Ligar verificação em duas etapas" }));
    fireEvent.change(await screen.findByLabelText(/código que o aplicativo mostra/), {
      target: { value: "123456" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirmar e ligar" }));
    fireEvent.click(await screen.findByRole("button", { name: "Copiar" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(CODIGOS.join("\n") + "\n"));
  });

  it("mostra o código recusado sem ligar nada", async () => {
    vi.mocked(api.totpStatus).mockResolvedValue(desligada);
    vi.mocked(api.totpSetup).mockResolvedValue({ secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/x" });
    vi.mocked(api.totpEnable).mockRejectedValue(
      new ApiError(401, "unauthorized", "código inválido", "code"),
    );
    render(<TwoFactor />);

    fireEvent.click(await screen.findByRole("button", { name: "Ligar verificação em duas etapas" }));
    fireEvent.change(await screen.findByLabelText(/código que o aplicativo mostra/), {
      target: { value: "000000" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Confirmar e ligar" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("código inválido");
    expect(screen.queryByRole("list", { name: "códigos de recuperação" })).not.toBeInTheDocument();
  });
});

describe("TwoFactor, ligada", () => {
  it("desliga com senha e código", async () => {
    vi.mocked(api.totpStatus).mockResolvedValueOnce(ligada).mockResolvedValue(desligada);
    vi.mocked(api.totpDisable).mockResolvedValue(undefined);
    const onChanged = vi.fn();
    render(<TwoFactor onChanged={onChanged} />);

    fireEvent.click(await screen.findByRole("button", { name: "Desligar" }));
    fireEvent.change(screen.getByLabelText(/^senha/), { target: { value: "uma-senha-bem-longa" } });
    fireEvent.change(screen.getByLabelText(/^código/), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Desligar" }));

    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(api.totpDisable).toHaveBeenCalledWith("uma-senha-bem-longa", "123456");
    expect(
      await screen.findByRole("button", { name: "Ligar verificação em duas etapas" }),
    ).toBeInTheDocument();
  });

  it("gera códigos novos e os mostra uma vez", async () => {
    vi.mocked(api.totpStatus).mockResolvedValue(ligada);
    vi.mocked(api.totpRecoveryCodes).mockResolvedValue({ recovery_codes: CODIGOS });
    render(<TwoFactor />);

    fireEvent.click(await screen.findByRole("button", { name: "Gerar códigos novos" }));
    fireEvent.change(screen.getByLabelText(/^senha/), { target: { value: "uma-senha-bem-longa" } });
    fireEvent.change(screen.getByLabelText(/^código/), { target: { value: "abcd-efgh-ijkm-npqr" } });
    fireEvent.click(screen.getByRole("button", { name: "Gerar códigos novos" }));

    expect(await screen.findByRole("list", { name: "códigos de recuperação" })).toBeInTheDocument();
    expect(api.totpRecoveryCodes).toHaveBeenCalledWith("uma-senha-bem-longa", "abcd-efgh-ijkm-npqr");
  });

  it("avisa quando sobram poucos códigos", async () => {
    vi.mocked(api.totpStatus).mockResolvedValue({ ...ligada, recovery_codes_left: 1 });
    render(<TwoFactor />);

    expect(await screen.findByText("Resta 1 código de recuperação.")).toHaveClass("text-down");
  });
});
