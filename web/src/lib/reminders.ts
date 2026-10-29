import type { Acknowledgement, Group } from "../api/types";
import { duration, elapsed } from "./format";

/**
 * Regras do lembrete de queda aberta e do "estou vendo".
 *
 * O intervalo tem três estados, e a tela precisa mantê-los separados:
 * null herda (do ambiente, do sistema, da instalação), 0 desliga e um
 * número relembra. Confundir null com 0 desligaria em silêncio o lembrete
 * que o sistema inteiro tinha ligado.
 */

/** Intervalos oferecidos, em minutos. O servidor aceita de 5 a 10080. */
export const INTERVALOS_LEMBRETE = [15, 30, 60, 120, 240, 480, 1440];

export type OpcaoLembrete = { value: string; label: string };

/**
 * opcoesLembrete monta a escolha do intervalo. O valor atual sempre
 * aparece, mesmo fora da lista: gravado pela API com 45 minutos, sumir da
 * tela faria a próxima edição trocá-lo sem ninguém pedir.
 */
export function opcoesLembrete(atual: number | null | undefined, herda: string): OpcaoLembrete[] {
  const minutos = [...INTERVALOS_LEMBRETE];
  if (atual != null && atual > 0 && !minutos.includes(atual)) {
    minutos.push(atual);
    minutos.sort((a, b) => a - b);
  }
  return [
    { value: "", label: herda },
    { value: "0", label: "desligado" },
    ...minutos.map((m) => ({ value: String(m), label: `a cada ${duration(m * 60)}` })),
  ];
}

/**
 * herdado diz o que "herdar" vai dar, subindo do grupo ao sistema. Sem a
 * resposta na tela, "herdar" é uma escolha às cegas: a pessoa não sabe se
 * está ligando um lembrete de hora em hora ou nenhum.
 */
export function herdado(grupoID: number | null | undefined, grupos: Group[], rotulo = "herdar"): string {
  const vistos = new Set<number>();
  for (let id = grupoID; id != null && !vistos.has(id); ) {
    vistos.add(id);
    const g = grupos.find((x) => x.id === id);
    if (!g) break;
    if (g.reminder_minutes != null) {
      const como = g.reminder_minutes === 0 ? "desligado" : `a cada ${duration(g.reminder_minutes * 60)}`;
      return `${rotulo} de ${g.name} (${como})`;
    }
    id = g.parent_id;
  }
  return `${rotulo} da instalação`;
}

/** valorLembrete leva o intervalo para o Select; null vira "". */
export function valorLembrete(minutos: number | null | undefined): string {
  return minutos == null ? "" : String(minutos);
}

/** lerLembrete traz de volta do Select; "" é herdar. */
export function lerLembrete(valor: string): number | null {
  return valor === "" ? null : Number(valor);
}

/**
 * vistoPor é a frase do reconhecimento: quem está olhando e há quanto
 * tempo. "Há quanto tempo" importa: visto há cinco horas, sem volta, é
 * sinal de que quem estava olhando pode ter parado.
 */
export function vistoPor(ack: Acknowledgement, now: Date = new Date()): string {
  return `visto por ${ack.by} há ${elapsed(ack.at, now)}`;
}
