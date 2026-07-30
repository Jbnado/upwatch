/**
 * Montagem da configuração de um canal de aviso.
 *
 * Fica fora do formulário porque é onde estão as decisões — o que vira
 * campo, o que é descartado, o que é recusado antes de sair da tela — e
 * porque assim elas podem ser testadas sem montar um formulário.
 *
 * Recusar aqui não substitui a validação do servidor: ela continua sendo a
 * que vale. O que se ganha é onde o erro aparece. O servidor responde
 * "config inválida", e a tela não teria como saber que o problema estava
 * no mapeamento e não na URL.
 */

export type LinhaCabecalho = { nome: string; valor: string };

export type EntradaConfig = {
  url: string;
  cabecalhos: LinhaCabecalho[];
  mapeamento: string;
};

export type ConfigCanal = {
  url: string;
  headers?: Record<string, string>;
  body_template?: unknown;
};

export type ResultadoConfig = {
  config?: ConfigCanal;
  erro?: { campo: string; msg: string };
};

export function montarConfig({ url, cabecalhos, mapeamento }: EntradaConfig): ResultadoConfig {
  const config: ConfigCanal = { url };

  const headers = montarCabecalhos(cabecalhos);
  if (headers) config.headers = headers;

  const bruto = mapeamento.trim();
  if (bruto !== "") {
    let corpo: unknown;
    try {
      corpo = JSON.parse(bruto);
    } catch (e) {
      return { erro: { campo: "mapeamento", msg: mensagemDeSintaxe(e) } };
    }

    // Objeto ou lista. Um número ou texto solto é JSON válido e nenhum
    // destino espera recebê-lo como corpo de alerta — recusar aqui evita
    // um canal que só falha na hora da queda.
    if (corpo === null || typeof corpo !== "object") {
      return {
        erro: {
          campo: "mapeamento",
          msg: "O mapeamento precisa ser um objeto ou uma lista.",
        },
      };
    }

    config.body_template = corpo;
  }

  return { config };
}

/**
 * montarCabecalhos transforma as linhas do formulário num objeto.
 *
 * Linha sem nome é descartada: o formulário começa com uma em branco só
 * para ter onde digitar, e mandá-la produziria um cabeçalho de nome vazio
 * — que alguns servidores recusam com um erro sem relação aparente com a
 * causa. Valor vazio, por outro lado, é legítimo e passa.
 */
function montarCabecalhos(linhas: LinhaCabecalho[]): Record<string, string> | undefined {
  const headers: Record<string, string> = {};

  for (const { nome, valor } of linhas) {
    const chave = nome.trim();
    if (chave === "") continue;
    headers[chave] = valor.trim();
  }

  return Object.keys(headers).length > 0 ? headers : undefined;
}

function mensagemDeSintaxe(e: unknown): string {
  const detalhe = e instanceof Error ? e.message : "";
  return detalhe ? `JSON inválido: ${detalhe}` : "JSON inválido.";
}
