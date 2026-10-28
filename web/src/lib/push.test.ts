import { describe, expect, it } from "vitest";
import { maskedPushAddress, pushAddress, pushSignal } from "./push";

/**
 * O endereço do push é a credencial do monitor: quem o tem mantém o alvo
 * "no ar" com o serviço parado. Por isso a tela mostra o endereço sem o
 * segredo inteiro, e copia o completo.
 */

describe("pushAddress", () => {
  it("monta o endereço completo e o mascarado", () => {
    const token = "AbCdEfGhIjKlMnOpQrSt";

    expect(pushAddress("https://up.exemplo", token)).toBe(
      "https://up.exemplo/api/v1/push/AbCdEfGhIjKlMnOpQrSt",
    );
    expect(maskedPushAddress("https://up.exemplo", token)).toBe(
      "https://up.exemplo/api/v1/push/AbCd…",
    );
  });
});

describe("pushSignal", () => {
  const agora = new Date(2026, 9, 28, 17, 0);

  it("diz quando o próximo sinal é esperado, se o servidor souber", () => {
    const linha = pushSignal({
      status: "up",
      nextExpected: new Date(2026, 9, 28, 18, 2).toISOString(),
      now: agora,
    });

    expect(linha).toEqual(["sinal em dia", "próximo esperado 18:02"]);
  });

  it("não promete horário que a API não deu", () => {
    // Sem agenda, o servidor não manda próximo esperado; inventar um a
    // partir do intervalo erraria justamente no push agendado.
    expect(pushSignal({ status: "unknown", now: agora })).toEqual(["nenhum sinal recebido ainda"]);
    expect(pushSignal({ status: "down", now: agora })).toEqual(["sinal atrasado"]);
  });
});
