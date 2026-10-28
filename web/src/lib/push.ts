import type { Status } from "../api/types";
import { sinceClock } from "./format";

/**
 * O monitor push, do lado da tela.
 *
 * O endereço é a credencial do monitor: quem o tem mantém o alvo "no ar"
 * com o serviço parado. A tela mostra o endereço com o segredo cortado e
 * copia o completo; o token inteiro só aparece por pedido explícito.
 */

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
 */
export function pushSignal({
  status,
  nextExpected,
  now = new Date(),
}: {
  status: Status;
  nextExpected?: string;
  now?: Date;
}): string[] {
  const partes = [
    status === "unknown"
      ? "nenhum sinal recebido ainda"
      : status === "up"
        ? "sinal em dia"
        : "sinal atrasado",
  ];
  if (nextExpected) partes.push(`próximo esperado ${sinceClock(nextExpected, now)}`);
  return partes;
}
