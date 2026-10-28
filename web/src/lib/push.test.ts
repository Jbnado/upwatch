import { describe, expect, it } from "vitest";
import { maskedPushAddress, pushAddress, pushSignal, pushTokenVisible } from "./push";

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

describe("pushSignal com fuso", () => {
  // Uma agenda escrita no fuso errado dispara na hora errada, e a tela só
  // mostra isso se disser a hora no fuso da agenda. No mesmo fuso de quem
  // olha, o horário curto basta.
  const agora = new Date(Date.UTC(2026, 9, 28, 12, 0));
  const proxima = new Date(Date.UTC(2026, 9, 28, 13, 2)).toISOString();

  it("mostra a hora no fuso da agenda quando ele não é o de quem olha", () => {
    const linha = pushSignal({
      status: "up",
      nextExpected: proxima,
      timezone: "Asia/Tokyo",
      localTimezone: "America/Sao_Paulo",
      now: agora,
    });

    expect(linha).toEqual(["sinal em dia", "próximo esperado 28/10 22:02 (Asia/Tokyo)"]);
  });

  it("fica com o horário curto no mesmo fuso de quem olha", () => {
    const linha = pushSignal({
      status: "up",
      nextExpected: proxima,
      timezone: "America/Sao_Paulo",
      localTimezone: "America/Sao_Paulo",
      now: agora,
    });

    expect(linha[1]).toMatch(/^próximo esperado \d{2}:\d{2}$/);
  });
});

describe("pushTokenVisible", () => {
  // Para quem só lê, o servidor devolve o token como "***": montar o
  // endereço com isso daria um endereço que não funciona.
  it("reconhece a máscara do servidor e o token vazio", () => {
    expect(pushTokenVisible("AbCdEfGhIjKlMnOpQrSt")).toBe(true);
    expect(pushTokenVisible("***")).toBe(false);
    expect(pushTokenVisible("")).toBe(false);
  });
});
