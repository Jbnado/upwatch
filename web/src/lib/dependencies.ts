import type { AffectedBy, Group, Monitor } from "../api/types";

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
 * candidatosAFilho são os monitores que podem passar a depender do pai:
 * nem ele, nem os ancestrais dele (fecharia ciclo), nem quem já depende.
 */
export function candidatosAFilho(pai: number, monitores: Monitor[]): Monitor[] {
  const proibidos = ancestrais(pai, monitores);
  proibidos.add(pai);
  return monitores
    .filter((m) => !proibidos.has(m.id) && m.parent_id !== pai)
    .sort(porNome);
}
