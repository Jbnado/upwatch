import type { ChannelTargets } from "../api/types";
import type { CheckState, ServiceNode } from "./service-tree";

/**
 * Onde um canal avisa, escolhido na árvore Sistema › Ambiente › serviço.
 *
 * A seleção vive nas folhas da árvore: os serviços e os grupos vazios,
 * pela chave do nó ("m10", "g5"). Grupo vazio é folha porque não tem o
 * que marcar dentro, e ligar o canal a ele antes do primeiro monitor é
 * justamente o caso de uso.
 *
 * O que se grava sai da forma da seleção, não de um clique em especial:
 * grupo com tudo marcado é vínculo com o grupo, e vale para o que for
 * criado nele depois; o resto marcado é vínculo com cada monitor. Assim a
 * caixa cheia sempre quer dizer a mesma coisa, tenha ela sido marcada de
 * uma vez ou serviço a serviço. Sistema com todos os ambientes cheios liga
 * o sistema, que cobre também os ambientes criados depois.
 */

const ehGrupo = (n: ServiceNode) => n.key.startsWith("g");
const idDoGrupo = (n: ServiceNode) => Number(n.key.slice(1));

/** folhas são as chaves marcáveis do nó para baixo, ele incluído. */
function folhas(n: ServiceNode): string[] {
  if (n.monitorId !== undefined) return [n.key, ...n.children.flatMap(folhas)];
  if (n.children.length === 0) return ehGrupo(n) ? [n.key] : [];
  return n.children.flatMap(folhas);
}

/**
 * chavesDoNo são as folhas que a caixa do nó governa. No serviço, só ele:
 * marcar a sonda não marca o que ela vigia, como na escolha de monitores.
 */
export function chavesDoNo(n: ServiceNode): string[] {
  return n.monitorId !== undefined ? [n.key] : folhas(n);
}

export function estadoDoAlvo(n: ServiceNode, sel: ReadonlySet<string>): CheckState {
  const chaves = chavesDoNo(n);
  const marcadas = chaves.filter((k) => sel.has(k)).length;
  if (marcadas === 0) return "unchecked";
  return marcadas === chaves.length ? "checked" : "mixed";
}

/** alternarAlvo esvazia a caixa cheia e completa a vazia ou parcial. */
export function alternarAlvo(n: ServiceNode, sel: ReadonlySet<string>): Set<string> {
  const chaves = chavesDoNo(n);
  const proximo = new Set(sel);
  if (estadoDoAlvo(n, sel) === "checked") chaves.forEach((k) => proximo.delete(k));
  else chaves.forEach((k) => proximo.add(k));
  return proximo;
}

/**
 * paraVinculos traduz a seleção no que se grava: o grupo mais alto com
 * tudo marcado vira vínculo com o grupo, e cada serviço marcado fora dele,
 * vínculo com o monitor. "Sem grupo" não é grupo de verdade, então os
 * serviços dele vão sempre um a um.
 */
export function paraVinculos(arvore: ServiceNode[], sel: ReadonlySet<string>): ChannelTargets {
  const grupos: number[] = [];
  const monitores: number[] = [];

  const visitar = (n: ServiceNode) => {
    if (ehGrupo(n)) {
      const todas = folhas(n);
      if (todas.length > 0 && todas.every((k) => sel.has(k))) {
        grupos.push(idDoGrupo(n));
        return;
      }
    }
    if (n.monitorId !== undefined && sel.has(n.key)) monitores.push(n.monitorId);
    n.children.forEach(visitar);
  };
  arvore.forEach(visitar);

  return { group_ids: grupos.sort((a, b) => a - b), monitor_ids: monitores.sort((a, b) => a - b) };
}

/**
 * deVinculos marca na árvore o que o canal já tem: tudo dentro de um
 * grupo ligado, ambientes inclusive, e cada monitor ligado.
 */
export function deVinculos(arvore: ServiceNode[], alvos: ChannelTargets | undefined): Set<string> {
  const sel = new Set<string>();
  if (!alvos) return sel;
  const grupos = new Set(alvos.group_ids);

  const visitar = (n: ServiceNode) => {
    if (ehGrupo(n) && grupos.has(idDoGrupo(n))) {
      folhas(n).forEach((k) => sel.add(k));
      return;
    }
    n.children.forEach(visitar);
  };
  arvore.forEach(visitar);
  alvos.monitor_ids.forEach((id) => sel.add(`m${id}`));
  return sel;
}

/**
 * foraDaArvore são os vínculos que a árvore não mostra, como um monitor
 * além do que a listagem trouxe. Voltam junto ao salvar: a tela não pode
 * desligar o que a pessoa nem viu.
 */
export function foraDaArvore(arvore: ServiceNode[], alvos: ChannelTargets | undefined): ChannelTargets {
  const vistos = new Set<string>();
  const visitar = (n: ServiceNode) => {
    vistos.add(n.key);
    n.children.forEach(visitar);
  };
  arvore.forEach(visitar);
  return {
    group_ids: (alvos?.group_ids ?? []).filter((id) => !vistos.has(`g${id}`)),
    monitor_ids: (alvos?.monitor_ids ?? []).filter((id) => !vistos.has(`m${id}`)),
  };
}

/** juntarVinculos soma dois conjuntos, sem repetidos e em ordem. */
export function juntarVinculos(a: ChannelTargets, b: ChannelTargets): ChannelTargets {
  const unir = (x: number[], y: number[]) => [...new Set([...x, ...y])].sort((p, q) => p - q);
  return { group_ids: unir(a.group_ids, b.group_ids), monitor_ids: unir(a.monitor_ids, b.monitor_ids) };
}

/** resumoDosAlvos diz, na lista de canais, onde cada um está ligado. */
export function resumoDosAlvos(alvos: ChannelTargets | undefined): string {
  const g = alvos?.group_ids.length ?? 0;
  const m = alvos?.monitor_ids.length ?? 0;
  if (g === 0 && m === 0) return "não está ligado a nada";
  const partes = [];
  if (g > 0) partes.push(`${g} ${g === 1 ? "grupo" : "grupos"}`);
  if (m > 0) partes.push(`${m} ${m === 1 ? "monitor" : "monitores"}`);
  return partes.join(", ");
}
