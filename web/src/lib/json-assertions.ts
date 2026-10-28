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
  { value: "absent", label: "não existe" },
] as const;

export type Operador = (typeof OPERADORES)[number]["value"];

export type LinhaAssercao = { caminho: string; operador: Operador; valor: string };

export type AssercaoJSON = { path: string; op: string; value?: unknown };

export type ResultadoAssercoes = { assercoes?: AssercaoJSON[]; erro?: string };

const ORDEM: ReadonlySet<string> = new Set(["lt", "lte", "gt", "gte"]);

const SEM_VALOR: ReadonlySet<string> = new Set(["exists", "absent"]);

/** semValor diz se o operador dispensa o valor esperado — e o recusa. */
export const semValor = (operador: string): boolean => SEM_VALOR.has(operador);

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

    if (semValor(operador)) {
      assercoes.push({ path, op: operador });
      continue;
    }

    const bruto = valor.trim();
    if (bruto === "") {
      return { erro: `Asserção ${n}: informe o valor esperado.` };
    }

    const inexato = numeroInexato(bruto);
    if (inexato !== undefined) {
      return {
        erro:
          `Asserção ${n}: o número ${inexato} não cabe exato em ponto flutuante de 64 bits ` +
          `e seria gravado alterado. Use um valor que caiba, ou compare como texto, entre aspas.`,
      };
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

/** Gramática de número JSON (RFC 8259), sem espaço em volta. */
const NUMERO_JSON = /-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/y;
const TEXTO_JSON = /"(?:[^"\\]|\\.)*"/y;

/**
 * numeroInexato devolve o primeiro número escrito em bruto — fora de
 * texto entre aspas — que JSON.parse não guardaria exato, ou undefined.
 *
 * O número é lido e gravado por JSON.parse e JSON.stringify, em float64:
 * 9007199254740993 vira ...992, e 1e999 vira Infinity, que é gravado como
 * null. Preservar o texto cru exigiria um parser JSON próprio no cliente
 * da API inteiro, porque a configuração volta do servidor por res.json(),
 * que já perdeu os dígitos. Então a tela recusa o que não sobrevive, e o
 * servidor recusa o mesmo no cadastro pela API — assim nada que a tela
 * leia volta alterado.
 */
function numeroInexato(bruto: string): string | undefined {
  // Texto que não é JSON vai como texto puro, e os dígitos dele são texto.
  try {
    JSON.parse(bruto);
  } catch {
    return undefined;
  }

  let i = 0;
  while (i < bruto.length) {
    TEXTO_JSON.lastIndex = i;
    const texto = TEXTO_JSON.exec(bruto);
    if (texto) {
      i += texto[0].length;
      continue;
    }
    NUMERO_JSON.lastIndex = i;
    const numero = NUMERO_JSON.exec(bruto);
    if (numero) {
      if (!sobreviveAoFloat64(numero[0])) return numero[0];
      i += numero[0].length;
      continue;
    }
    i++;
  }
  return undefined;
}

/**
 * sobreviveAoFloat64 diz se o número, escrito como texto, tem o mesmo
 * valor da grafia que JSON.stringify daria depois de JSON.parse. 0.1 não
 * é exato em binário, mas volta como 0.1 e por isso passa.
 */
function sobreviveAoFloat64(texto: string): boolean {
  const n = Number(texto);
  return Number.isFinite(n) && canonico(texto) === canonico(String(n));
}

/**
 * canonico reduz a grafia de um número a sinal, dígitos significativos e
 * expoente, para 340.0, 3.4e2 e 340 compararem iguais sem passar por
 * Number.
 */
function canonico(texto: string): string {
  const m = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(texto);
  if (!m) return texto;
  const [, sinal, inteira, fracao = "", expoente = "0"] = m;
  let digitos = (inteira + fracao).replace(/^0+/, "");
  if (digitos === "") return "0";
  let exp = BigInt(expoente) - BigInt(fracao.length);
  const semZeros = digitos.replace(/0+$/, "");
  exp += BigInt(digitos.length - semZeros.length);
  digitos = semZeros;
  return `${sinal}${digitos}e${exp}`;
}

export function lerAssercoes(config: Record<string, unknown> | undefined): LinhaAssercao[] {
  const lista = config?.["json_assertions"];
  if (!Array.isArray(lista) || lista.length === 0) return [linhaVazia()];

  const linhas = (lista as AssercaoJSON[]).map((a) => ({
    caminho: a.path,
    operador: a.op as Operador,
    valor: semValor(a.op) ? "" : exibir(a.value),
  }));
  return [...linhas, linhaVazia()];
}

/**
 * exibir é o inverso de interpretar: o texto que, lido de novo, dá o
 * mesmo valor. Texto vai cru só quando, lido de novo, volta igual e cabe
 * no campo de uma linha; senão vai entre aspas. Sem isso, "100" voltaria
 * como 100, "" viraria campo vazio — recusado como valor esquecido — e uma
 * quebra de linha sumiria no campo.
 */
function exibir(value: unknown): string {
  if (
    typeof value === "string" &&
    value !== "" &&
    value === value.trim() &&
    !/[\u0000-\u001f\u007f]/.test(value) &&
    interpretar(value) === value
  ) {
    return value;
  }
  return JSON.stringify(value);
}

export function aplicarAssercoes(
  config: Record<string, unknown> | undefined,
  assercoes: AssercaoJSON[],
): Record<string, unknown> {
  const { json_assertions: _, ...resto } = config ?? {};
  return assercoes.length > 0 ? { ...resto, json_assertions: assercoes } : resto;
}
