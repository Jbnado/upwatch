/**
 * O que sobrou do cálculo de série na interface: nada.
 *
 * Estado, disponibilidade, percentis e a divisão da janela em buckets vêm
 * prontos de `/api/v1/summaries`. Enquanto viviam aqui, existiam em duas
 * cópias — painel e tela de detalhe — que discordavam sobre o que fazer
 * com ausência de medição sem que nada acusasse, e o mesmo período rendia
 * dois números conforme quem perguntasse.
 *
 * Este arquivo guarda só a pergunta que é da interface: "faz tempo demais
 * que ninguém verifica?". Ela depende do relógio de quem olha, e por isso
 * é a única que não faz sentido responder no servidor.
 */

/**
 * isStale diz se o monitor deixou de ser verificado.
 *
 * Duas janelas sem notícia já é anomalia: o agendador deveria ter passado
 * e não passou. É falha do UpWatch, não do alvo, e sem o aviso ela se
 * disfarça de "tudo estável" — o pior modo de falhar para uma ferramenta
 * de vigilância.
 *
 * Recebe o instante da última verificação, que o servidor informa como
 * fato sobre o monitor, independente da janela olhada. Antes recebia o
 * carimbo da última amostra desenhada, e isso errava de dois jeitos: em
 * bucket agregado o carimbo é o início do período, e na série crua era o
 * fim de uma fatia truncada — o que chegou a acusar de abandonado um
 * monitor verificado segundos antes.
 */
export function isStale(
  lastCheckAt: string | null | undefined,
  intervalSeconds: number,
  now: Date = new Date(),
): boolean {
  if (!lastCheckAt) return false;

  return now.getTime() - new Date(lastCheckAt).getTime() > intervalSeconds * 2000;
}
