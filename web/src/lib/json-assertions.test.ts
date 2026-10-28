import { describe, expect, it } from "vitest";
import {
  aplicarAssercoes,
  lerAssercoes,
  montarAssercoes,
  type LinhaAssercao,
} from "./json-assertions";

/**
 * A montagem das asserções sobre o corpo JSON de um monitor HTTP.
 *
 * Fica fora do formulário pelo mesmo motivo da configuração de canal: é
 * onde estão as decisões — como o texto digitado vira valor tipado, o que
 * é descartado, o que é recusado antes de sair da tela — e o servidor só
 * saberia responder "config inválida", sem dizer qual linha.
 */

const linha = (caminho: string, operador: LinhaAssercao["operador"], valor = ""): LinhaAssercao => ({
  caminho,
  operador,
  valor,
});

describe("montarAssercoes", () => {
  // O servidor compara tipo: "100" e 100 são diferentes. Quem digita 100
  // num campo de texto quer o número, e quem digita up quer o texto — a
  // tela não pode obrigar a escrever "up" entre aspas para o caso comum.
  it("interpreta o valor digitado com o tipo que ele aparenta", () => {
    const r = montarAssercoes([
      linha("$.db", "eq", "up"),
      linha("$.queue_lag", "lt", "100"),
      linha("$.ready", "eq", "true"),
      linha("$.version", "eq", "null"),
      linha("$.codigo", "eq", '"100"'),
    ]);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual([
      { path: "$.db", op: "eq", value: "up" },
      { path: "$.queue_lag", op: "lt", value: 100 },
      { path: "$.ready", op: "eq", value: true },
      { path: "$.version", op: "eq", value: null },
      { path: "$.codigo", op: "eq", value: "100" },
    ]);
  });

  // O servidor recusa exists com valor; a tela nem mostra o campo, mas
  // um valor esquecido de quando o operador era outro não pode ir junto.
  it("não manda valor em exists", () => {
    const r = montarAssercoes([linha("$.db", "exists", "sobra de antes")]);

    expect(r.assercoes).toEqual([{ path: "$.db", op: "exists" }]);
  });

  // A lista começa com uma linha em branco para ter onde digitar.
  it("descarta linha sem caminho", () => {
    const r = montarAssercoes([linha("", "eq", ""), linha("  ", "eq", "x")]);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual([]);
  });

  it("recusa comparação de ordem com valor que não é número, apontando a linha", () => {
    const r = montarAssercoes([linha("$.db", "eq", "up"), linha("$.lag", "lt", "cem")]);

    expect(r.assercoes).toBeUndefined();
    expect(r.erro).toMatch(/2/);
    expect(r.erro).toMatch(/número/);
  });

  // Valor vazio em eq seria interpretado como texto vazio, e quase sempre
  // é campo esquecido — não a intenção de exigir "".
  it("recusa operador sem valor", () => {
    const r = montarAssercoes([linha("$.db", "eq", "  ")]);

    expect(r.erro).toMatch(/valor/);
  });

  it("recusa caminho que não começa com $", () => {
    const r = montarAssercoes([linha("db", "eq", "up")]);

    expect(r.erro).toMatch(/\$/);
  });

  // absent, como exists, não leva valor: o servidor recusaria a asserção.
  it("não manda valor em absent", () => {
    const r = montarAssercoes([linha("$.debug", "absent", "sobra de antes")]);

    expect(r.assercoes).toEqual([{ path: "$.debug", op: "absent" }]);
  });

  // JSON.parse guarda número em float64. 9007199254740993 viraria ...992
  // e 1e999 viraria Infinity, que JSON.stringify manda como null: a tela
  // gravaria em silêncio uma asserção diferente da digitada. Também dentro
  // de lista e objeto, que passam pelo mesmo JSON.parse.
  it.each([
    "9007199254740993",
    "1e999",
    "-1e999",
    "0.1000000000000000000001",
    "1e-400",
    '{"id": 9007199254740993}',
    "[1, 1e999]",
  ])("recusa número que float64 não guarda exato: %s", (valor) => {
    const r = montarAssercoes([linha("$.id", "eq", valor)]);

    expect(r.assercoes).toBeUndefined();
    expect(r.erro).toMatch(/1/);
    expect(r.erro).toMatch(/64 bits/);
  });

  // O que float64 guarda passa, inclusive os que não são exatos em binário
  // mas voltam com a mesma grafia — 0.1 é o caso comum de limiar.
  it.each([
    ["0.1", 0.1],
    ["9007199254740992", 9007199254740992],
    ["1e308", 1e308],
    ["340.0", 340],
    ["-5", -5],
  ])("aceita número que float64 guarda: %s", (valor, esperado) => {
    const r = montarAssercoes([linha("$.n", "eq", valor)]);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual([{ path: "$.n", op: "eq", value: esperado }]);
  });

  // Número dentro de texto é texto: não pode ser recusado por parecer
  // grande.
  // Valor que não é JSON é texto puro, e os dígitos dele são texto.
  it("não confunde dígitos de texto puro com número", () => {
    const r = montarAssercoes([linha("$.id", "eq", "pedido-9007199254740993")]);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual([{ path: "$.id", op: "eq", value: "pedido-9007199254740993" }]);
  });

  it("não confunde número dentro de texto com número", () => {
    const r = montarAssercoes([linha("$.o", "eq", '{"id": "9007199254740993"}')]);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual([{ path: "$.o", op: "eq", value: { id: "9007199254740993" } }]);
  });
});

describe("lerAssercoes", () => {
  // Abrir para editar e salvar sem mexer não pode mudar o tipo: o texto
  // "100" precisa voltar entre aspas, senão a segunda gravação o
  // transforma em número.
  it("volta ao texto que, montado de novo, dá o mesmo valor", () => {
    const original = [
      { path: "$.db", op: "eq", value: "up" },
      { path: "$.codigo", op: "eq", value: "100" },
      { path: "$.lag", op: "lt", value: 100 },
      { path: "$.v", op: "eq", value: null },
      { path: "$.tags", op: "contains", value: { id: 1 } },
      { path: "$.db", op: "exists" },
    ];

    const linhas = lerAssercoes({ json_assertions: original });
    const r = montarAssercoes(linhas);

    expect(r.assercoes).toEqual(original);
  });

  // Cada valor precisa voltar ao mesmo valor depois de exibido e montado
  // de novo. "" exibido cru seria campo vazio, que a montagem recusa como
  // valor esquecido; e um texto com quebra de linha exibido cru perderia a
  // quebra no campo de uma linha só.
  it.each<[string, unknown]>([
    ["texto vazio", ""],
    ["quebra de linha", "\n"],
    ["texto com quebra no meio", "a\nb"],
    ["texto com espaço nas pontas", " up "],
    ["texto que parece número", "100"],
    ["número", 100],
    ["booleano", true],
    ["nulo", null],
  ])("ida e volta preservam %s", (_, value) => {
    const original = [{ path: "$.x", op: "eq", value }];

    const linhas = lerAssercoes({ json_assertions: original });
    const r = montarAssercoes(linhas);

    expect(r.erro).toBeUndefined();
    expect(r.assercoes).toEqual(original);
    expect(linhas[0]?.valor).not.toMatch(/\n/);
  });

  it("devolve uma linha em branco quando não há asserções", () => {
    expect(lerAssercoes(undefined)).toEqual([linha("", "eq", "")]);
  });
});

describe("aplicarAssercoes", () => {
  // O formulário só edita as asserções. Perder os cabeçalhos ou o
  // expect_status gravados pela API ao salvar pela tela seria destruir
  // configuração que a pessoa nem está vendo.
  it("preserva o resto da configuração", () => {
    const config = aplicarAssercoes({ expect_status: [401], headers: { A: "b" } }, [
      { path: "$.db", op: "exists" },
    ]);

    expect(config).toEqual({
      expect_status: [401],
      headers: { A: "b" },
      json_assertions: [{ path: "$.db", op: "exists" }],
    });
  });

  it("remove a chave quando a lista fica vazia", () => {
    const config = aplicarAssercoes({ json_assertions: [{ path: "$", op: "exists" }], body: "x" }, []);

    expect(config).toEqual({ body: "x" });
  });
});
