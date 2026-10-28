import type { Channel, Group, Scope } from "../api/types";
import { crumb } from "./groups";

/**
 * Acesso por conta e origem dos avisos, do lado da tela.
 *
 * A barreira está no servidor. O que mora aqui é a montagem da escolha e
 * a frase que a resume, e as duas precisam ser fiéis ao que o servidor
 * vai aplicar.
 */

/** scopeLabel resume o acesso de uma conta numa linha. */
export function scopeLabel(scope: Scope, groups: Group[]): string {
  if (scope.all_groups) return "Todos os grupos";

  const nomes = scope.group_ids
    .map((id) => groups.find((g) => g.id === id))
    .filter((g): g is Group => g !== undefined)
    .map((g) => crumb(g.path))
    .sort((a, b) => a.localeCompare(b, "pt-BR"));

  return nomes.length > 0 ? nomes.join(", ") : "Nenhum grupo";
}

/** covers diz se a lista concede o grupo, direto ou pelo sistema dele. */
export function covers(ids: number[], group: Group): boolean {
  return ids.includes(group.id) || (group.parent_id !== null && ids.includes(group.parent_id));
}

/**
 * toggleScopeGroup marca ou desmarca um grupo na lista.
 *
 * Marcar o sistema tira os ambientes dele da lista: o sistema já os
 * concede, e guardá-los à parte deixaria acesso para trás quando o
 * sistema fosse desmarcado. Ambiente coberto pelo sistema não se mexe.
 */
export function toggleScopeGroup(ids: number[], id: number, groups: Group[]): number[] {
  if (ids.includes(id)) return ids.filter((x) => x !== id);

  const alvo = groups.find((g) => g.id === id);
  if (alvo && covers(ids, alvo)) return ids;

  const filhos = new Set(groups.filter((g) => g.parent_id === id).map((g) => g.id));
  return [...ids.filter((x) => !filhos.has(x)), id];
}

/**
 * canRenameGroup diz se o servidor aceita renomear o grupo.
 *
 * Renomear exige enxergar todos os irmãos do grupo: o conflito de nome
 * com um irmão de fora revelaria que ele existe, e o servidor recusa com
 * 403 seja o nome livre ou não. Os irmãos de um sistema são todos os
 * sistemas, que só a conta de escopo total vê. Os de um ambiente são os
 * do mesmo sistema, que a conta vê quando tem o sistema — e é quando ele
 * aparece na lista de grupos dela, que o servidor já recorta.
 */
export function canRenameGroup(scope: Scope, group: Group, visible: Group[]): boolean {
  if (scope.all_groups) return true;
  if (group.parent_id === null) return false;
  return visible.some((g) => g.id === group.parent_id);
}

/**
 * canLinkChannels diz se a conta vincula canais a monitores e grupos.
 * O canal é de todos os grupos: pendurá-lo no próprio grupo é mexer no
 * aviso dos outros, e o servidor recusa com 403 quem não alcança tudo.
 */
export function canLinkChannels(scope: Scope): boolean {
  return scope.all_groups;
}

/** NO_CHANNEL_LINK_HINT explica, na tela, por que não há o que marcar. */
export const NO_CHANNEL_LINK_HINT =
  "Só quem tem acesso a todos os grupos vincula canais: o canal avisa sobre monitores de vários grupos, e mudar onde ele vale mexe no aviso dos outros.";

export type ChannelSource = { origin: string; channels: Channel[] };

/**
 * channelOrigins junta os canais do monitor, do ambiente e do sistema,
 * um por linha, com todas as origens de cada um. O servidor avisa uma vez
 * só por canal, e a tela conta a mesma história.
 */
export function channelOrigins(
  sources: ChannelSource[],
): { channel: Channel; origins: string[] }[] {
  const porID = new Map<number, { channel: Channel; origins: string[] }>();

  for (const { origin, channels } of sources) {
    for (const channel of channels) {
      const linha = porID.get(channel.id);
      if (linha) linha.origins.push(origin);
      else porID.set(channel.id, { channel, origins: [origin] });
    }
  }
  return [...porID.values()];
}
