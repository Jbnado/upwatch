import type { AffectedBy, Group, Monitor } from "../api/types";
import type { LockedBox } from "./service-tree";

/**
 * Monitores dependentes, do lado da tela.
 *
 * A regra de quem cala quem é do servidor (o motor e a API usam a mesma);
 * aqui só se decide o que oferecer no cadastro e como dizer que um
 * monitor está calado.
 */

/** rotuloCalado diz por que o monitor está sem sinal próprio. */
export function rotuloCalado(ref: AffectedBy): string {
  return ref.name ? `sem sinal — ${ref.name} fora` : "sem sinal — dependência fora";
}

/** filhosDe lista os monitores que dependem diretamente do pai. */
export function filhosDe(pai: number, monitores: Monitor[]): Monitor[] {
  return monitores.filter((m) => m.parent_id === pai);
}

/**
 * descendentes reúne quem depende do monitor, direta ou indiretamente.
 *
 * O conjunto de visitados também protege de um ciclo antigo no banco,
 * que faria a busca girar para sempre.
 */
function descendentes(id: number, monitores: Monitor[]): Set<number> {
  const achados = new Set<number>();
  const fila = [id];
  while (fila.length > 0) {
    const atual = fila.pop()!;
    for (const m of monitores) {
      if (m.parent_id === atual && !achados.has(m.id) && m.id !== id) {
        achados.add(m.id);
        fila.push(m.id);
      }
    }
  }
  return achados;
}

/** ancestrais reúne os pais do monitor, subindo até o topo. */
function ancestrais(id: number, monitores: Monitor[]): Set<number> {
  const porID = new Map(monitores.map((m) => [m.id, m]));
  const achados = new Set<number>();
  for (let p = porID.get(id)?.parent_id; p != null && !achados.has(p); p = porID.get(p)?.parent_id) {
    achados.add(p);
  }
  return achados;
}

const nomes = new Intl.Collator("pt-BR", { sensitivity: "base", numeric: true });
const porNome = (a: Monitor, b: Monitor) => nomes.compare(a.name, b.name);

/** ehSonda reconhece o grupo das sondas pelo nome, sem acento nem caixa. */
function ehSonda(g: Group | undefined): boolean {
  if (!g) return false;
  const nome = g.path.normalize("NFD").replace(/\p{Diacritic}/gu, "").toLowerCase();
  return nome.includes("sonda");
}

/**
 * opcoesDePai monta a escolha do pai no cadastro.
 *
 * Os sugeridos vêm primeiro: o sinal de vida (push) de um grupo de
 * sondas, que é o pai do caso comum — a verificação que uma sonda faz
 * numa rede privada. O resto continua escolhível. O próprio monitor e
 * quem depende dele ficam de fora: escolhê-los fecharia um ciclo, que o
 * servidor recusaria.
 */
export function opcoesDePai(
  monitores: Monitor[],
  grupos: Group[],
  proprio?: number,
): { sugeridos: Monitor[]; outros: Monitor[] } {
  const proibidos = proprio === undefined ? new Set<number>() : descendentes(proprio, monitores);
  if (proprio !== undefined) proibidos.add(proprio);
  const grupoDe = new Map(grupos.map((g) => [g.id, g]));

  const sugeridos: Monitor[] = [];
  const outros: Monitor[] = [];
  for (const m of monitores) {
    if (proibidos.has(m.id)) continue;
    const sugerido = m.type === "push" && ehSonda(m.group_id != null ? grupoDe.get(m.group_id) : undefined);
    (sugerido ? sugeridos : outros).push(m);
  }
  return { sugeridos: sugeridos.sort(porNome), outros: outros.sort(porNome) };
}

/**
 * aninharDependentes põe cada item sob o pai, quando o pai está na mesma
 * lista. É a forma de toda lista de monitores: o painel, a lista do grupo
 * e os seletores.
 *
 * Quem tem o pai fora da lista fica na raiz: o pai noutro grupo, ou
 * escondido pela busca, não pode levar o filho junto para fora da tela.
 * Um ciclo antigo no banco também fica na raiz, senão os dois sumiriam
 * um dentro do outro. A ordem de entrada é mantida em cada nível.
 */
export function aninharDependentes<T>(
  itens: T[],
  monitorDe: (item: T) => Monitor,
): { raiz: T[]; filhos: Map<number, T[]> } {
  const presentes = new Map(itens.map((i) => [monitorDe(i).id, monitorDe(i)]));
  const emCiclo = (m: Monitor) => {
    const vistos = new Set<number>();
    for (let p = m.parent_id; p != null && presentes.has(p); p = presentes.get(p)!.parent_id) {
      if (p === m.id) return true;
      // Ciclo acima dele, entre os ancestrais: ele mesmo pode aninhar.
      if (vistos.has(p)) return false;
      vistos.add(p);
    }
    return false;
  };

  const raiz: T[] = [];
  const filhos = new Map<number, T[]>();
  for (const item of itens) {
    const m = monitorDe(item);
    if (m.parent_id == null || !presentes.has(m.parent_id) || emCiclo(m)) {
      raiz.push(item);
      continue;
    }
    filhos.set(m.parent_id, [...(filhos.get(m.parent_id) ?? []), item]);
  }
  return { raiz, filhos };
}

/**
 * travasDeFilho são as caixas que não se marcam ao ligar dependentes ao
 * pai: ele e os ancestrais dele (fechariam ciclo), vazios, e quem já
 * depende dele, marcados. Ficam à vista, com o porquê, em vez de sumir
 * da árvore: sumidos, a pessoa procuraria o monitor sem achar.
 */
export function travasDeFilho(pai: number, monitores: Monitor[]): Map<number, LockedBox> {
  const travas = new Map<number, LockedBox>();
  for (const m of filhosDe(pai, monitores)) travas.set(m.id, { checked: true, reason: "já depende deste" });
  for (const id of ancestrais(pai, monitores)) travas.set(id, { checked: false, reason: "este depende dele" });
  travas.set(pai, { checked: false, reason: "é este monitor" });
  return travas;
}
