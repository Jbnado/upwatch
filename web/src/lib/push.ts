import type { Status } from "../api/types";
import { sinceClock } from "./format";
import { fusoLocal, proximaExecucao } from "./push-config";

/**
 * O monitor push, do lado da tela.
 *
 * O endereço é a credencial do monitor: quem o tem mantém o alvo "no ar"
 * com o serviço parado. A tela mostra o endereço com o segredo cortado e
 * copia o completo; o token inteiro só aparece por pedido explícito.
 */

/**
 * pushTokenVisible diz se o token veio inteiro. Para quem só lê, o
 * servidor manda "***" no lugar dele, e um endereço montado com a máscara
 * não funcionaria — a tela mostra o sinal e deixa o endereço de fora.
 */
export function pushTokenVisible(token: string): boolean {
  return token !== "" && token !== "***";
}

export function pushAddress(origin: string, token: string): string {
  return `${origin}/api/v1/push/${token}`;
}

export function maskedPushAddress(origin: string, token: string): string {
  return `${origin}/api/v1/push/${token.slice(0, 4)}…`;
}

/**
 * pushSignal descreve o sinal em pedaços para a linha do cabeçalho.
 *
 * O instante do último sinal ainda não sai na API; quando sair, entra
 * aqui como primeiro pedaço ("último sinal há 40 s"). Até lá o estado diz
 * o que se sabe, e o próximo esperado só aparece quando o servidor o
 * calcula (push com agenda).
 *
 * O próximo esperado sai no fuso da agenda quando ele não é o de quem
 * olha: uma agenda escrita no fuso errado dispara na hora errada, e só a
 * hora no fuso dela deixa isso à vista agora, e não na primeira
 * madrugada em que o aviso deveria ter chegado.
 */
export function pushSignal({
  status,
  nextExpected,
  timezone,
  localTimezone = fusoLocal(),
  now = new Date(),
}: {
  status: Status;
  nextExpected?: string;
  /** Fuso da agenda do monitor. */
  timezone?: string;
  /** Fuso de quem olha; o do navegador, fora dos testes. */
  localTimezone?: string;
  now?: Date;
}): string[] {
  const partes = [
    status === "unknown"
      ? "nenhum sinal recebido ainda"
      : status === "up"
        ? "sinal em dia"
        : "sinal atrasado",
  ];
  if (nextExpected) {
    const quando =
      timezone && timezone !== localTimezone
        ? proximaExecucao(nextExpected, timezone)
        : sinceClock(nextExpected, now);
    partes.push(`próximo esperado ${quando}`);
  }
  return partes;
}
