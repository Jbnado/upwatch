import { describe, expect, it } from "vitest";
import { lerPush, montarPushConfig, proximaExecucao } from "./push-config";

/**
 * A configuração do monitor push no formulário.
 *
 * Vive separada da tela porque é onde estão as decisões: o que vai para
 * o servidor, o que é descartado e o que precisa sobreviver a uma edição.
 */

describe("montarPushConfig", () => {
  // Sem agenda, o monitor espera por intervalo. Mandar schedule vazio ou
  // um fuso solto faria o servidor guardar campos que não significam nada.
  it("manda só o token quando não há agenda nem folga", () => {
    expect(montarPushConfig("tok", { schedule: "", timezone: "America/Sao_Paulo", folga: 0 })).toEqual({
      token: "tok",
    });
  });

  it("manda agenda com fuso e folga", () => {
    expect(
      montarPushConfig("tok", { schedule: " 2 9,18 * * * ", timezone: "America/Sao_Paulo", folga: 600 }),
    ).toEqual({
      token: "tok",
      schedule: "2 9,18 * * *",
      timezone: "America/Sao_Paulo",
      grace_period_seconds: 600,
    });
  });

  it("manda a folga também sem agenda", () => {
    expect(montarPushConfig("tok", { schedule: "", timezone: "UTC", folga: 120 })).toEqual({
      token: "tok",
      grace_period_seconds: 120,
    });
  });
});

describe("lerPush", () => {
  // Editar o nome não pode apagar a agenda nem a folga: o formulário
  // antigo mandava só o token de volta, e a folga configurada sumia na
  // primeira edição.
  it("devolve o que estava salvo, para a edição não apagar nada", () => {
    const r = lerPush(
      { token: "tok", schedule: "0 3 * * *", timezone: "Europe/Lisbon", grace_period_seconds: 900 },
      "America/Sao_Paulo",
    );

    expect(r).toEqual({
      token: "tok",
      agenda: { schedule: "0 3 * * *", timezone: "Europe/Lisbon", folga: 900 },
    });
  });

  // Rotinas são agendadas no horário local; partir do fuso de quem está
  // cadastrando acerta o caso comum sem esconder o campo.
  it("sugere o fuso de quem cadastra quando não há um salvo", () => {
    const r = lerPush({ token: "tok" }, "America/Sao_Paulo");

    expect(r.agenda).toEqual({ schedule: "", timezone: "America/Sao_Paulo", folga: 0 });
  });

  it("aguenta configuração ausente", () => {
    expect(lerPush(undefined, "UTC")).toEqual({
      token: "",
      agenda: { schedule: "", timezone: "UTC", folga: 0 },
    });
  });
});

describe("proximaExecucao", () => {
  // A execução é mostrada no fuso do monitor, não no do navegador: é o
  // horário que está escrito no crontab, e é comparando os dois que se
  // percebe um fuso errado.
  it("mostra o horário no fuso do monitor, com o fuso por extenso", () => {
    expect(proximaExecucao("2026-07-28T21:02:00Z", "America/Sao_Paulo")).toBe(
      "28/07 18:02 (America/Sao_Paulo)",
    );
  });

  it("aguenta fuso desconhecido mostrando UTC", () => {
    expect(proximaExecucao("2026-07-28T21:02:00Z", "Lugar/Nenhum")).toBe("28/07 21:02 (UTC)");
  });
});
