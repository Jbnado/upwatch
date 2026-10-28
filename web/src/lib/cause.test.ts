import { describe, expect, it } from "vitest";
import { causeKey, explainCause } from "./cause";

/**
 * A causa crua vem do Go, às vezes do sistema operacional, às vezes em
 * inglês, às vezes no idioma do Windows de quem roda o UpWatch. Quem está
 * de plantão precisa ler "conexão recusada", não decifrar connectex.
 *
 * A regra que mais importa aqui é a última: o que não casa com nenhum
 * padrão aparece como veio. Um resumo inventado é pior que o texto feio,
 * porque manda a pessoa investigar a coisa errada.
 */

const casos: [string, string, string][] = [
  // [nome do caso, causa crua, resumo esperado]
  [
    "recusada no Windows em português",
    'Get "http://127.0.0.1:1/health": dial tcp 127.0.0.1:1: connectex: Nenhuma conexão pôde ser feita porque a máquina de destino as recusou ativamente.',
    "conexão recusada",
  ],
  [
    "recusada no Windows em inglês",
    "dial tcp 10.0.0.5:8080: connectex: No connection could be made because the target machine actively refused it.",
    "conexão recusada",
  ],
  ["recusada no Linux", "dial tcp 10.0.0.5:8080: connect: connection refused", "conexão recusada"],
  ["recusada por código", "connect ECONNREFUSED 10.0.0.5:8080", "conexão recusada"],
  [
    "tempo esgotado do cliente HTTP",
    'Get "https://api.interno/": context deadline exceeded (Client.Timeout exceeded while awaiting headers)',
    "tempo esgotado",
  ],
  ["tempo esgotado na conexão", "dial tcp 10.0.0.1:443: i/o timeout", "tempo esgotado"],
  [
    "tempo esgotado no Windows em português",
    "dial tcp 10.0.0.1:443: connectex: Uma tentativa de conexão falhou porque o componente conectado não respondeu corretamente após um período de tempo ou a conexão estabelecida falhou porque o host conectado não respondeu.",
    "tempo esgotado",
  ],
  [
    "tempo esgotado no Windows em inglês",
    "dial tcp 10.0.0.1:443: connectex: A connection attempt failed because the connected party did not properly respond after a period of time.",
    "tempo esgotado",
  ],
  [
    "nome não resolvido",
    "dial tcp: lookup api.interno on 127.0.0.53:53: no such host",
    "nome não resolvido (DNS)",
  ],
  [
    "resolvedor com problema",
    "dial tcp: lookup api.interno on 10.0.0.2:53: server misbehaving",
    "nome não resolvido (DNS)",
  ],
  ["nome inexistente no checker de DNS", "resolvedor respondeu NXDOMAIN", "nome não resolvido (DNS)"],
  [
    "redefinida no Linux",
    "read tcp 10.0.0.9:51234->10.0.0.5:443: read: connection reset by peer",
    "conexão redefinida pelo servidor",
  ],
  [
    "redefinida no Windows em português",
    "read tcp 10.0.0.9:51234->10.0.0.5:443: wsarecv: Foi forçado o cancelamento de uma conexão existente pelo host remoto.",
    "conexão redefinida pelo servidor",
  ],
  [
    "redefinida no Windows em inglês",
    "wsarecv: An existing connection was forcibly closed by the remote host.",
    "conexão redefinida pelo servidor",
  ],
  [
    "certificado vencido do Go",
    'Get "https://x/": tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2026-10-28T13:00:00Z is after 2026-10-01T00:00:00Z',
    "certificado expirado",
  ],
  ["certificado vencido do checker TLS", "certificado expirou em 2026-10-01T00:00:00Z", "certificado expirado"],
  [
    "certificado ainda não válido",
    "x509: certificate has expired or is not yet valid: current time 2026-10-28T13:00:00Z is before 2026-11-01T00:00:00Z",
    "certificado ainda não é válido",
  ],
  [
    "certificado com nome errado do Go",
    "tls: failed to verify certificate: x509: certificate is valid for a.exemplo, not b.exemplo",
    "certificado com nome errado",
  ],
  [
    "certificado com nome errado do checker TLS",
    'certificado não vale para o nome "b.exemplo": x509: certificate is valid for a.exemplo, not b.exemplo',
    "certificado com nome errado",
  ],
  [
    "autoridade desconhecida",
    "tls: failed to verify certificate: x509: certificate signed by unknown authority",
    "certificado inválido (autoridade desconhecida)",
  ],
  ["outro erro de x509", "x509: malformed certificate", "certificado inválido"],
  ["status inesperado", "status 503 fora do esperado", "respondeu 503"],
  ['corpo sem o termo', 'corpo não contém "ok"', "corpo não confere"],
  ["corpo com termo proibido", 'corpo contém o termo proibido "erro"', "corpo não confere"],
  ["corpo fora da expressão", 'corpo não casa com "^ok$"', "corpo não confere"],
  ["push em silêncio", "sem sinal há 3m41s, acima da janela de 40s", "sem sinal"],
];

describe("explainCause", () => {
  it.each(casos)("resume %s", (_nome, cru, resumo) => {
    const got = explainCause(cru);

    expect(got.summary).toBe(resumo);
    expect(got.known).toBe(true);
    // O texto técnico nunca se perde: é ele que vai para o chamado.
    expect(got.raw).toBe(cru);
  });

  it("devolve cru o que não reconhece, sem inventar", () => {
    for (const cru of [
      "nenhum registro A para api.interno",
      "pânico no checker http: nil map",
      "resposta em 1834ms, acima do limiar de 1s",
    ]) {
      expect(explainCause(cru)).toEqual({ summary: cru, raw: cru, known: false });
    }
  });

  it("trata ausência de causa como ausência", () => {
    expect(explainCause("")).toEqual({ summary: "", raw: "", known: false });
  });
});

describe("causeKey", () => {
  it("junta a mesma causa com porta efêmera e instante diferentes", () => {
    // Cada tentativa sai por uma porta local nova, e o push conta o
    // silêncio em segundos: sem normalizar, nenhuma falha seria igual à
    // anterior e o agrupamento nunca aconteceria.
    expect(causeKey("read tcp 10.0.0.9:51234->10.0.0.5:443: read: connection reset by peer")).toBe(
      causeKey("read tcp 10.0.0.9:60001->10.0.0.5:443: read: connection reset by peer"),
    );
    expect(causeKey("sem sinal há 3m41s, acima da janela de 40s")).toBe(
      causeKey("sem sinal há 3m21s, acima da janela de 40s"),
    );
    expect(causeKey("nenhum registro A para x às 13:20:51")).toBe(
      causeKey("nenhum registro A para x às 13:22:56"),
    );
  });

  it("separa causas diferentes", () => {
    expect(causeKey("status 503 fora do esperado")).not.toBe(causeKey("status 502 fora do esperado"));
    expect(causeKey("dial tcp: i/o timeout")).not.toBe(causeKey("connect: connection refused"));
  });
});
