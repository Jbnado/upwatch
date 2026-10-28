/**
 * Tradução da causa de uma falha para quem está de plantão.
 *
 * A causa chega como o Go a escreveu, muitas vezes com a mensagem do
 * sistema operacional dentro — em inglês no Linux, no idioma do Windows de
 * quem roda o UpWatch. "connectex: Nenhuma conexão pôde ser feita…" e
 * "connect: connection refused" são a mesma coisa, e quem lê no meio de um
 * incidente precisa ler a coisa, não o dialeto.
 *
 * Só se traduz o que se reconhece com certeza. O resto aparece como veio:
 * um resumo inventado manda a pessoa investigar a causa errada, e isso é
 * pior que um texto feio.
 */

export type Cause = {
  /** Resumo curto em português; igual ao cru quando não se reconhece. */
  summary: string;
  /** Texto técnico completo, como veio do servidor. */
  raw: string;
  /** Se algum padrão casou. Sem isso, não há o que mostrar em segundo plano. */
  known: boolean;
};

type Padrao = { teste: RegExp; resumo: string | ((m: RegExpMatchArray) => string) };

/**
 * PADROES em ordem: o primeiro que casa vence. Certificado vem antes de
 * tudo porque a frase do x509 carrega horários e nomes que poderiam casar
 * com outro padrão. Uma consulta de nome que expira ("lookup …: i/o
 * timeout") fica como tempo esgotado: é o que a mensagem afirma.
 */
const PADROES: Padrao[] = [
  // Certificado: o Go diz "is after" para vencido e "is before" para o
  // que ainda não vale, na mesma frase de erro.
  { teste: /certificado expirou|x509: certificate has expired.*is after/i, resumo: "certificado expirado" },
  {
    teste: /certificado só passa a valer|x509: certificate has expired or is not yet valid.*is before/i,
    resumo: "certificado ainda não é válido",
  },
  { teste: /x509: certificate has expired/i, resumo: "certificado expirado ou ainda não válido" },
  {
    teste: /certificado não vale para o nome|x509: certificate is (valid for|not valid for)/i,
    resumo: "certificado com nome errado",
  },
  {
    teste: /x509: certificate signed by unknown authority/i,
    resumo: "certificado inválido (autoridade desconhecida)",
  },
  { teste: /x509:|cadeia de certificação inválida/i, resumo: "certificado inválido" },

  // Conexão recusada: nada escutando na porta.
  {
    teste: /connection refused|ECONNREFUSED|actively refused|recusou ativamente|recusou a conexão/i,
    resumo: "conexão recusada",
  },

  // Conexão derrubada pelo outro lado no meio da conversa.
  {
    teste: /connection reset|ECONNRESET|forcibly closed|cancelamento de uma conexão existente/i,
    resumo: "conexão redefinida pelo servidor",
  },

  // Nome que não resolveu.
  {
    teste: /no such host|server misbehaving|ENOTFOUND|EAI_NONAME|name resolution|resolvedor respondeu NXDOMAIN/i,
    resumo: "nome não resolvido (DNS)",
  },

  // Tempo esgotado, nas várias vozes.
  {
    teste:
      /i\/o timeout|context deadline exceeded|Client\.Timeout exceeded|ETIMEDOUT|timed out|did not properly respond|não respondeu corretamente/i,
    resumo: "tempo esgotado",
  },

  // Mensagens do próprio UpWatch, já em português mas longas.
  { teste: /^status (\d{3}) fora do esperado/, resumo: (m) => `respondeu ${m[1]}` },
  { teste: /^corpo (não contém|contém o termo proibido|não casa com) /, resumo: "corpo não confere" },
  { teste: /^sem sinal há .* acima da janela/, resumo: "sem sinal" },
];

export function explainCause(raw: string): Cause {
  for (const p of PADROES) {
    const m = raw.match(p.teste);
    if (m) {
      const summary = typeof p.resumo === "string" ? p.resumo : p.resumo(m);
      return { summary, raw, known: true };
    }
  }
  return { summary: raw, raw, known: false };
}

/**
 * causeKey é a identidade de uma causa para agrupar falhas seguidas.
 *
 * Reconhecida, é o resumo. Não reconhecida, é o texto com os números
 * apagados: porta efêmera, segundos de silêncio e horários mudam a cada
 * tentativa sem mudar a causa.
 */
export function causeKey(raw: string): string {
  const c = explainCause(raw);
  return c.known ? c.summary : raw.replace(/\d+/g, "#");
}
