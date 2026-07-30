import { describe, expect, it } from "vitest";
import { montarConfig, type LinhaCabecalho } from "./channel-config";

/**
 * A montagem da configuração do canal.
 *
 * Vive separada do formulário porque é onde estão as decisões: o que vira
 * campo, o que é descartado e o que é recusado antes de sair da tela. Um
 * mapeamento malformado recusado aqui mostra o erro embaixo do campo; o
 * mesmo erro vindo do servidor chega como "config inválida" sem dizer onde.
 */

const cab = (nome: string, valor: string): LinhaCabecalho => ({ nome, valor });

describe("montarConfig", () => {
  it("manda só a url quando nada mais foi preenchido", () => {
    const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: "" });

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  it("transforma as linhas de cabeçalho num objeto", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("Authorization", "Bearer abc"), cab("X-Origem", "upwatch")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ Authorization: "Bearer abc", "X-Origem": "upwatch" });
  });

  // O formulário começa com uma linha em branco para ter onde digitar.
  // Mandá-la produziria um cabeçalho de nome vazio, que alguns servidores
  // recusam com 400 — e o erro não teria relação aparente com a causa.
  it("descarta linha de cabeçalho em branco", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("", ""), cab("X-Key", "k"), cab("  ", "sem nome")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "k" });
  });

  it("apara espaço em volta do nome do cabeçalho", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("  X-Key  ", "  k  ")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "k" });
  });

  // Um cabeçalho sem valor é legítimo; um sem nome não é.
  it("mantém cabeçalho de valor vazio", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("X-Vazio", "")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Vazio": "" });
  });

  it("não manda o campo de cabeçalhos quando todas as linhas estão vazias", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("", ""), cab("", "")],
      mapeamento: "",
    });

    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  it("aceita mapeamento válido", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [],
      mapeamento: '{"event":"$status"}',
    });

    expect(r.erro).toBeUndefined();
    expect(r.config?.body_template).toEqual({ event: "$status" });
  });

  it("recusa mapeamento que não é JSON, apontando o campo", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [],
      mapeamento: '{"event": }',
    });

    expect(r.config).toBeUndefined();
    expect(r.erro?.campo).toBe("mapeamento");
    expect(r.erro?.msg).toBeTruthy();
  });

  // Só espaço em branco é o mesmo que não ter preenchido.
  it("ignora mapeamento em branco", () => {
    const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: "   \n " });

    expect(r.erro).toBeUndefined();
    expect(r.config).toEqual({ url: "https://x.exemplo" });
  });

  // O corpo precisa ser um objeto ou uma lista. Um número solto é JSON
  // válido e nenhum destino espera receber "7" como corpo de alerta —
  // recusar aqui evita um canal que só falha na hora da queda.
  it("recusa mapeamento que não é objeto nem lista", () => {
    for (const bruto of ["7", '"texto"', "true", "null"]) {
      const r = montarConfig({ url: "https://x.exemplo", cabecalhos: [], mapeamento: bruto });
      expect(r.erro?.campo, `aceitou ${bruto}`).toBe("mapeamento");
    }
  });

  it("mantém a ordem quando dois cabeçalhos têm o mesmo nome, o último vence", () => {
    const r = montarConfig({
      url: "https://x.exemplo",
      cabecalhos: [cab("X-Key", "primeiro"), cab("X-Key", "segundo")],
      mapeamento: "",
    });

    expect(r.config?.headers).toEqual({ "X-Key": "segundo" });
  });
});
