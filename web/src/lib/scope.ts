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
