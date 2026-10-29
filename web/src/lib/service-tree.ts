import type { Group, Monitor } from "../api/types";
import { aninharDependentes } from "./dependencies";
import { crumb } from "./groups";

/**
 * Árvore Sistema › Ambiente › serviço › dependentes, para escolher
 * monitores.
 *
 * Serve à página pública (o que publicar), ao relato (o que foi afetado)
 * e a ligar dependentes. Depois dos grupos o nome só é único dentro do
 * ambiente, então a escolha precisa mostrar de onde cada serviço vem.
 *
 * O dependente fica sob o pai quando os dois estão no mesmo grupo. Com o
 * pai noutro grupo, fica no grupo dele: marcar "RPA › prod" tem de levar
 * tudo o que está em RPA/prod, e não o que está pendurado numa sonda de
 * outro lugar.
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
  /** Só nos serviços: quem é, para dizer de quem ele depende. */
  monitor?: Monitor;
  /** No grupo, os serviços; no serviço, os dependentes dele. */
  children: ServiceNode[];
};

export type CheckState = "checked" | "unchecked" | "mixed";

/** Caixa que o clique não muda, com o porquê ao lado. */
export type LockedBox = { checked: boolean; reason: string };

const NOMES = new Intl.Collator("pt-BR", { numeric: true, sensitivity: "base" });
const porNome = (a: { name: string }, b: { name: string }) => NOMES.compare(a.name, b.name);

const SEM_GRUPO = "Sem grupo";

/** servicos monta os serviços de um grupo, cada dependente sob o pai. */
function servicos(monitors: Monitor[], path: string): ServiceNode[] {
  const { raiz, filhos } = aninharDependentes([...monitors].sort(porNome), (m) => m);
  const no = (m: Monitor): ServiceNode => ({
    key: `m${m.id}`,
    name: m.name,
    path,
    monitorId: m.id,
    monitor: m,
    children: (filhos.get(m.id) ?? []).map(no),
  });
  return raiz.map(no);
}

/** buildServiceTree monta a árvore; grupo sem monitor fica de fora. */
export function buildServiceTree(groups: Group[], monitors: Monitor[]): ServiceNode[] {
  const conhecidos = new Set(groups.map((g) => g.id));
  const doGrupo = (id: number) =>
    servicos(
      monitors.filter((m) => m.group_id === id),
      crumb(groups.find((g) => g.id === id)!.path),
    );

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

  const semGrupo = servicos(
    monitors.filter((m) => m.group_id == null || !conhecidos.has(m.group_id)),
    SEM_GRUPO,
  );
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
 * algum. "banco rpa prod" acha o banco certo entre os quatro. O pai de um
 * dependente achado continua, para o achado não aparecer fora do lugar.
 */
export function filterServiceTree(nodes: ServiceNode[], query: string): ServiceNode[] {
  const termos = semAcento(query).split(/\s+/).filter(Boolean);
  if (termos.length === 0) return nodes;

  const filtrar = (lista: ServiceNode[]): ServiceNode[] =>
    lista.flatMap((n) => {
      const filhos = filtrar(n.children);
      const achou =
        n.monitorId !== undefined &&
        termos.every((t) => semAcento(`${n.name} ${n.path}`).includes(t));
      return achou || filhos.length > 0 ? [{ ...n, children: filhos }] : [];
    });
  return filtrar(nodes);
}

/** subtreeIds são todos os monitores do nó para baixo, ele incluído. */
export function subtreeIds(node: ServiceNode): number[] {
  const proprio = node.monitorId !== undefined ? [node.monitorId] : [];
  return [...proprio, ...node.children.flatMap(subtreeIds)];
}

/**
 * leafIds são os monitores que a caixa do nó governa, menos os travados.
 *
 * No grupo, todos os de dentro, dependentes inclusive. No serviço, só
 * ele: marcar a sonda não marca as verificações dela, porque publicar ou
 * avisar sobre a sonda não é o mesmo que sobre o que ela vigia.
 */
export function leafIds(node: ServiceNode, locked: ReadonlySet<number> = new Set()): number[] {
  const ids = node.monitorId !== undefined ? [node.monitorId] : subtreeIds(node);
  return ids.filter((id) => !locked.has(id));
}

/**
 * nodeState é a caixa do nó: cheia, vazia ou parcial. Os travados não
 * contam: a caixa fala só do que um clique nela muda.
 */
export function nodeState(
  node: ServiceNode,
  selected: Set<number>,
  locked: ReadonlySet<number> = new Set(),
): CheckState {
  const ids = leafIds(node, locked);
  const marcados = ids.filter((id) => selected.has(id)).length;
  if (marcados === 0) return "unchecked";
  return marcados === ids.length ? "checked" : "mixed";
}

/**
 * toggleNode devolve a seleção depois de clicar no nó. Caixa cheia
 * esvazia; vazia ou parcial completa — clicar numa parcial e ver sumir o
 * que foi escolhido um a um seria punir quem já começou.
 */
export function toggleNode(
  node: ServiceNode,
  selected: Set<number>,
  locked: ReadonlySet<number> = new Set(),
): Set<number> {
  const ids = leafIds(node, locked);
  const proximo = new Set(selected);
  if (nodeState(node, selected, locked) === "checked") ids.forEach((id) => proximo.delete(id));
  else ids.forEach((id) => proximo.add(id));
  return proximo;
}

/**
 * isCollapsible: recolhe o grupo que tem serviços direto e o serviço que
 * tem dependentes. Sistema com ambientes não recolhe — é só o título.
 */
export function isCollapsible(node: ServiceNode): boolean {
  if (node.monitorId !== undefined) return node.children.length > 0;
  return node.children.some((c) => c.monitorId !== undefined);
}

/** openByDefault são os nós recolhíveis com algo marcado dentro. */
export function openByDefault(nodes: ServiceNode[], selected: Set<number>): Set<string> {
  const abertos = new Set<string>();
  const visitar = (n: ServiceNode) => {
    const dentro = n.monitorId !== undefined ? n.children.flatMap(subtreeIds) : subtreeIds(n);
    if (isCollapsible(n) && dentro.some((id) => selected.has(id))) abertos.add(n.key);
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
