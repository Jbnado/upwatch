/**
 * Asserções sobre o corpo JSON de um monitor HTTP.
 *
 * Como em channel-config, fica fora do formulário porque é onde estão as
 * decisões — como o texto digitado vira valor tipado, o que é descartado,
 * o que é recusado antes de sair da tela — e assim elas podem ser testadas
 * sem montar um formulário. A validação que vale continua sendo a do
 * servidor; o que se ganha aqui é o erro apontar a linha.
 */

export const OPERADORES = [
  { value: "eq", label: "é igual a" },
  { value: "ne", label: "é diferente de" },
  { value: "lt", label: "é menor que" },
  { value: "lte", label: "é menor ou igual a" },
  { value: "gt", label: "é maior que" },
  { value: "gte", label: "é maior ou igual a" },
  { value: "contains", label: "contém" },
  { value: "exists", label: "existe" },
] as const;

export type Operador = (typeof OPERADORES)[number]["value"];

export type LinhaAssercao = { caminho: string; operador: Operador; valor: string };

export type AssercaoJSON = { path: string; op: string; value?: unknown };

export type ResultadoAssercoes = { assercoes?: AssercaoJSON[]; erro?: string };

const ORDEM: ReadonlySet<string> = new Set(["lt", "lte", "gt", "gte"]);

export const linhaVazia = (): LinhaAssercao => ({ caminho: "", operador: "eq", valor: "" });

export function montarAssercoes(linhas: LinhaAssercao[]): ResultadoAssercoes {
  const assercoes: AssercaoJSON[] = [];

  for (const [i, { caminho, operador, valor }] of linhas.entries()) {
    const path = caminho.trim();
    if (path === "") continue;

    const n = i + 1;
    if (!path.startsWith("$")) {
      return { erro: `Asserção ${n}: o caminho começa com $, como em $.status.` };
    }

    if (operador === "exists") {
      assercoes.push({ path, op: operador });
      continue;
    }

    const bruto = valor.trim();
    if (bruto === "") {
      return { erro: `Asserção ${n}: informe o valor esperado.` };
    }

    const value = interpretar(bruto);
    if (ORDEM.has(operador) && typeof value !== "number") {
      return { erro: `Asserção ${n}: maior e menor comparam só número.` };
    }
    assercoes.push({ path, op: operador, value });
  }

  return { assercoes };
}

/**
 * interpretar lê o valor como JSON quando ele é JSON — número, true,
 * false, null, texto entre aspas — e como texto puro quando não é. Assim
 * o caso comum (up, 100) dispensa aspas, e o raro ("100" como texto) ainda
 * é possível.
 */
function interpretar(bruto: string): unknown {
  try {
    return JSON.parse(bruto);
  } catch {
    return bruto;
  }
}

export function lerAssercoes(config: Record<string, unknown> | undefined): LinhaAssercao[] {
  const lista = config?.["json_assertions"];
  if (!Array.isArray(lista) || lista.length === 0) return [linhaVazia()];

  const linhas = (lista as AssercaoJSON[]).map((a) => ({
    caminho: a.path,
    operador: a.op as Operador,
    valor: a.op === "exists" ? "" : exibir(a.value),
  }));
  return [...linhas, linhaVazia()];
}

/**
 * exibir é o inverso de interpretar: o texto que, lido de novo, dá o
 * mesmo valor. Um texto que pareceria número volta entre aspas — sem
 * isso, abrir e salvar sem mexer trocaria "100" por 100.
 */
function exibir(value: unknown): string {
  if (typeof value === "string" && interpretar(value.trim()) === value) return value;
  return JSON.stringify(value);
}

export function aplicarAssercoes(
  config: Record<string, unknown> | undefined,
  assercoes: AssercaoJSON[],
): Record<string, unknown> {
  const { json_assertions: _, ...resto } = config ?? {};
  return assercoes.length > 0 ? { ...resto, json_assertions: assercoes } : resto;
}
