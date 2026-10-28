import type { Group, Monitor } from "../api/types";
import { crumb } from "./groups";

/**
 * Árvore Sistema › Ambiente › serviço, para escolher monitores.
 *
 * Serve à página pública (o que publicar) e ao relato (o que foi
 * afetado). Depois dos grupos o nome só é único dentro do ambiente, então
 * a escolha precisa mostrar de onde cada serviço vem.
 *
 * A seleção vive nos serviços. Marcar um grupo marca os serviços que ele
 * tem agora, e só eles: diferente do acesso por conta, publicar não é
 * dinâmico. Um serviço criado depois num ambiente "publicado" não pode
 * aparecer sozinho na página que o cliente lê.
 */

export type ServiceNode = {
  /** "g<id>" para grupo, "sem-grupo", "m<id>" para serviço. */
  key: string;
  name: string;
  /** Caminho do grupo como a interface o escreve, "Retaguarda · prod". */
  path: string;
  /** Só nos serviços. */
  monitorId?: number;
  children: ServiceNode[];
};

export type CheckState = "checked" | "unchecked" | "mixed";

const NOMES = new Intl.Collator("pt-BR", { numeric: true, sensitivity: "base" });
const porNome = (a: { name: string }, b: { name: string }) => NOMES.compare(a.name, b.name);

const SEM_GRUPO = "Sem grupo";

function folha(m: Monitor, path: string): ServiceNode {
  return { key: `m${m.id}`, name: m.name, path, monitorId: m.id, children: [] };
}

/** buildServiceTree monta a árvore; grupo sem monitor fica de fora. */
export function buildServiceTree(groups: Group[], monitors: Monitor[]): ServiceNode[] {
  const conhecidos = new Set(groups.map((g) => g.id));
  const doGrupo = (id: number) =>
    monitors
      .filter((m) => m.group_id === id)
      .map((m) => folha(m, crumb(groups.find((g) => g.id === id)!.path)))
      .sort(porNome);

  const ambiente = (g: Group, name: string): ServiceNode => ({
    key: `g${g.id}`,
    name,
    path: crumb(g.path),
    children: doGrupo(g.id),
  });

  const sistemas = groups
    .filter((g) => g.parent_id === null)
    .map((s) => ({
      key: `g${s.id}`,
      name: s.name,
      path: crumb(s.path),
      children: [
        ...groups
          .filter((g) => g.parent_id === s.id)
          .map((g) => ambiente(g, g.name))
          .filter((n) => n.children.length > 0)
          .sort(porNome),
        ...doGrupo(s.id),
      ],
    }));

  // Ambiente cujo sistema a conta não enxerga: vai pelo caminho inteiro,
  // porque "prod" sozinho no topo não diria de qual sistema é.
  const soltos = groups
    .filter((g) => g.parent_id !== null && !conhecidos.has(g.parent_id))
    .map((g) => ambiente(g, crumb(g.path)));

  const topo = [...sistemas, ...soltos].filter((n) => n.children.length > 0).sort(porNome);

  const semGrupo = monitors
    .filter((m) => m.group_id == null || !conhecidos.has(m.group_id))
    .map((m) => folha(m, SEM_GRUPO))
    .sort(porNome);
  if (semGrupo.length > 0) {
    topo.push({ key: "sem-grupo", name: SEM_GRUPO, path: SEM_GRUPO, children: semGrupo });
  }
  return topo;
}

function semAcento(texto: string): string {
  return texto.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();
}

/**
 * filterServiceTree deixa os serviços em que cada palavra da busca
 * aparece no nome ou no caminho do grupo, e os grupos que ficaram com
 * algum. "banco rpa prod" acha o banco certo entre os quatro.
 */
export function filterServiceTree(nodes: ServiceNode[], query: string): ServiceNode[] {
  const termos = semAcento(query).split(/\s+/).filter(Boolean);
  if (termos.length === 0) return nodes;

  const filtrar = (lista: ServiceNode[]): ServiceNode[] =>
    lista.flatMap((n) => {
      if (n.monitorId !== undefined) {
        const texto = semAcento(`${n.name} ${n.path}`);
        return termos.every((t) => texto.includes(t)) ? [n] : [];
      }
      const filhos = filtrar(n.children);
      return filhos.length > 0 ? [{ ...n, children: filhos }] : [];
    });
  return filtrar(nodes);
}

/** leafIds são os monitores sob o nó, em qualquer profundidade. */
export function leafIds(node: ServiceNode): number[] {
  if (node.monitorId !== undefined) return [node.monitorId];
  return node.children.flatMap(leafIds);
}

/** nodeState é a caixa do nó: cheia, vazia ou parcial. */
export function nodeState(node: ServiceNode, selected: Set<number>): CheckState {
  const ids = leafIds(node);
  const marcados = ids.filter((id) => selected.has(id)).length;
  if (marcados === 0) return "unchecked";
  return marcados === ids.length ? "checked" : "mixed";
}

/**
 * toggleNode devolve a seleção depois de clicar no nó. Caixa cheia
 * esvazia; vazia ou parcial completa — clicar numa parcial e ver sumir o
 * que foi escolhido um a um seria punir quem já começou.
 */
export function toggleNode(node: ServiceNode, selected: Set<number>): Set<number> {
  const ids = leafIds(node);
  const proximo = new Set(selected);
  if (nodeState(node, selected) === "checked") ids.forEach((id) => proximo.delete(id));
  else ids.forEach((id) => proximo.add(id));
  return proximo;
}

/** isCollapsible: só o grupo que tem serviços direto se recolhe. */
export function isCollapsible(node: ServiceNode): boolean {
  return node.children.some((c) => c.monitorId !== undefined);
}

/** openByDefault são os grupos recolhíveis que já têm algo marcado. */
export function openByDefault(nodes: ServiceNode[], selected: Set<number>): Set<string> {
  const abertos = new Set<string>();
  const visitar = (n: ServiceNode) => {
    if (n.monitorId !== undefined) return;
    if (isCollapsible(n) && nodeState(n, selected) !== "unchecked") abertos.add(n.key);
    n.children.forEach(visitar);
  };
  nodes.forEach(visitar);
  return abertos;
}

/** serviceOrigin é o grupo de onde o monitor vem, para dizer ao lado dele. */
export function serviceOrigin(m: Monitor, groups: Group[]): string {
  const g = groups.find((x) => x.id === m.group_id);
  return g ? crumb(g.path) : SEM_GRUPO;
}
