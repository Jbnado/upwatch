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
  template?: string;
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

// ---------- e-mail ----------

export type SegurancaSmtp = "starttls" | "tls" | "none";

export type EntradaEmail = {
  host: string;
  porta: string;
  seguranca: SegurancaSmtp;
  usuario: string;
  senha: string;
  remetente: string;
  destinatarios: string;
};

export type ConfigEmail = {
  host: string;
  port?: number;
  security: SegurancaSmtp;
  username?: string;
  password?: string;
  from: string;
  to: string[];
};

export function montarConfigEmail(e: EntradaEmail): {
  config?: ConfigEmail;
  erro?: { campo: string; msg: string };
} {
  // Lista de distribuição costuma vir colada de outro lugar; vírgula,
  // ponto e vírgula e quebra de linha valem igual.
  const to = e.destinatarios
    .split(/[,;\n]/)
    .map((d) => d.trim())
    .filter((d) => d !== "");
  if (to.length === 0) {
    return { erro: { campo: "destinatarios", msg: "Informe ao menos um destinatário." } };
  }

  const config: ConfigEmail = {
    host: e.host.trim(),
    security: e.seguranca,
    from: e.remetente.trim(),
    to,
  };

  // Em branco, o servidor escolhe pela segurança: 587, 465 ou 25.
  const porta = e.porta.trim();
  if (porta !== "") {
    const n = Number(porta);
    if (!/^\d+$/.test(porta) || n < 1 || n > 65535) {
      return { erro: { campo: "porta", msg: "A porta vai de 1 a 65535." } };
    }
    config.port = n;
  }

  const usuario = e.usuario.trim();
  if (usuario !== "") config.username = usuario;
  // A senha vai como foi digitada: espaço nas pontas é parte dela.
  if (e.senha !== "") config.password = e.senha;

  return { config };
}

// ---------- ntfy ----------

export type EntradaNtfy = { servidor: string; topico: string; token: string };

export type ConfigNtfy = { server?: string; topic: string; token?: string };

/** O formato de tópico que o ntfy aceita. */
const TOPICO_NTFY = /^[A-Za-z0-9_-]{1,64}$/;

/**
 * Diz se o canal vai publicar no ntfy.sh público, onde o tópico é a única
 * credencial. Em branco conta: é o servidor que vale sem nada preenchido.
 */
export function ntfyServidorPublico(servidor: string): boolean {
  const s = servidor.trim();
  if (s === "") return true;
  try {
    return new URL(s).hostname.toLowerCase() === "ntfy.sh";
  } catch {
    return false;
  }
}

export function montarConfigNtfy(e: EntradaNtfy): {
  config?: ConfigNtfy;
  erro?: { campo: string; msg: string };
} {
  const topic = e.topico.trim();
  if (!TOPICO_NTFY.test(topic)) {
    return {
      erro: {
        campo: "topico",
        msg: "Use só letras sem acento, números, - e _ (até 64).",
      },
    };
  }

  const config: ConfigNtfy = { topic };
  const server = e.servidor.trim();
  if (server !== "") config.server = server;
  const token = e.token.trim();
  if (token !== "") config.token = token;
  return { config };
}
