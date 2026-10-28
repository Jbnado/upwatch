import type { GroupStatus, GroupSummary, MonitorType, Status } from "../api/types";

/**
 * Regras de grupo que a interface aplica sobre o que o servidor manda.
 *
 * O servidor já recorta pelo escopo e já soma os ambientes no sistema;
 * aqui só se decide ordem e palavra, que são perguntas de quem olha.
 */

/**
 * PROBLEM_RANK é a urgência de cada estado, a mesma do servidor
 * (domain.ProblemRank). Repetida aqui porque a lista do grupo ordena o que
 * a própria tela já tem, sem pedir de novo. O grupo vazio não tem nada
 * errado, como o que está no ar.
 */
export const PROBLEM_RANK: Record<GroupStatus, number> = {
  down: 3,
  degraded: 2,
  unknown: 1,
  up: 0,
  empty: 0,
};

const NOMES = new Intl.Collator("pt-BR", { numeric: true, sensitivity: "base" });

/**
 * byProblem põe o que exige ação no topo: fora do ar, degradado, sem
 * medição e por fim no ar. Dentro de cada estado, nome em ordem natural.
 * Pausado vai para o fim, porque o estado dele é de antes da pausa.
 */
export function byProblem<T>(
  items: T[],
  get: (item: T) => { status: GroupStatus; name: string; paused?: boolean },
): T[] {
  return [...items].sort((a, b) => {
    const x = get(a);
    const y = get(b);
    if (Boolean(x.paused) !== Boolean(y.paused)) return x.paused ? 1 : -1;
    const rank = PROBLEM_RANK[y.status] - PROBLEM_RANK[x.status];
    if (rank !== 0) return rank;
    return NOMES.compare(x.name, y.name);
  });
}

/**
 * cardWord é a palavra única do cartão.
 *
 * Uma palavra e não uma tabela de contagens: o cartão responde "preciso
 * entrar aqui?", e o detalhe mora uma tela adiante.
 */
export function cardWord(s: GroupSummary): string {
  if (s.status === "empty" || s.total === 0) return "vazio";
  if (s.total === s.paused) return "pausado";
  if (s.problems === 0) return "ok";
  if (s.down === 0 && s.unknown === 0) return "lento";
  if (s.down === 0 && s.degraded === 0) return "sem medição";
  return s.problems === 1 ? "1 problema" : `${s.problems} problemas`;
}

export type CardSection = {
  /** Nome do sistema; vazio para a seção de "Sem grupo". */
  title: string;
  cards: GroupSummary[];
};

/**
 * organizeCards monta as seções da tela inicial: um bloco por sistema,
 * um cartão por ambiente.
 *
 * O cartão do próprio sistema só aparece quando ele não tem ambientes —
 * ao lado dos ambientes, ele contaria cada problema duas vezes. O sistema
 * de um ambiente é achado pelo pai e, se o escopo não alcança o pai, pelo
 * caminho: quem vê só "Retaguarda / prod" ainda precisa ler "Retaguarda".
 */
export function organizeCards(items: GroupSummary[]): CardSection[] {
  const secoes = new Map<string, CardSection>();
  const comAmbiente = new Set(items.map((i) => i.parent_id).filter((id) => id !== null));

  const secao = (chave: string, title: string) => {
    let s = secoes.get(chave);
    if (!s) {
      s = { title, cards: [] };
      secoes.set(chave, s);
    }
    return s;
  };

  for (const item of items) {
    if (item.group_id === null) continue;

    if (item.parent_id === null) {
      const s = secao(`g${item.group_id}`, item.name);
      if (!comAmbiente.has(item.group_id)) s.cards.push(item);
      continue;
    }

    const pai = items.find((i) => i.group_id === item.parent_id);
    const titulo = pai?.name ?? item.path.split(" / ")[0] ?? item.path;
    secao(`g${item.parent_id}`, titulo).cards.push(item);
  }

  const resultado = [...secoes.values()].filter((s) => s.cards.length > 0);

  const semGrupo = items.find((i) => i.group_id === null);
  if (semGrupo) resultado.push({ title: "", cards: [semGrupo] });

  return resultado;
}

/**
 * hasGroups diz se a instalação usa grupos. Sem nenhum, a tela inicial é
 * a lista de sempre: um cartão solitário de "Sem grupo" só acrescentaria
 * um clique.
 */
export function hasGroups(items: GroupSummary[]): boolean {
  return items.some((i) => i.group_id !== null);
}

const SEPARADOR = /^([^\s\-_/·:.]+)[\s\-_/·:.]+/;

function normal(texto: string): string {
  return texto
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]/g, "");
}

/**
 * shortName tira do começo do nome as palavras que repetem o grupo.
 *
 * Antes dos grupos, o ambiente ia no nome ("rpa-prod bff"). Dentro de
 * "RPA · prod", repetir isso em toda linha é ruído. Só sai palavra
 * inteira, e o nome nunca fica vazio.
 */
export function shortName(name: string, path: string): string {
  const partes = path.split(" / ").map(normal).filter(Boolean);
  if (partes.length === 0) return name;

  let resto = name;
  for (;;) {
    const m = SEPARADOR.exec(resto);
    if (!m || !partes.includes(normal(m[1]!))) break;
    resto = resto.slice(m[0].length);
  }

  // Sobrar só o nome do grupo ("rpa prod" em RPA · prod) também é ficar
  // sem nome: aí a palavra é o próprio monitor, e o original fica.
  const curto = resto.trim();
  return curto === "" || partes.includes(normal(curto)) ? name : curto;
}

/** crumb escreve o caminho do grupo como a interface o mostra. */
export function crumb(path: string): string {
  if (!path) return "Sem grupo";
  return path.split(" / ").join(" · ");
}

/**
 * problemWord é o estado dito em palavra numa linha de problema. No push
 * quem bate é o serviço, então o que falta é sinal, e não o alvo no ar.
 */
export function problemWord(status: Status, type: MonitorType): string {
  if (type === "push") {
    if (status === "down") return "sem sinal";
    if (status === "unknown") return "aguardando sinal";
  }
  switch (status) {
    case "down":
      return "fora do ar";
    case "degraded":
      return "lento";
    case "unknown":
      return "sem medição";
    case "up":
      return "no ar";
  }
}

/** Recorte da lista por estado; pausado é um estado à parte. */
export type StateFilter = "todos" | Status | "pausado";

/**
 * matches diz se a linha entra na busca e no recorte. A busca olha nome e
 * alvo, ignorando caixa e acento: quem procura "conciliacao" no teclado
 * de plantão não deveria ficar sem resultado por causa de um til.
 */
export function matches(
  item: { name: string; target?: string; status: Status; paused?: boolean },
  query: string,
  state: StateFilter,
): boolean {
  if (state === "pausado" && !item.paused) return false;
  if (state !== "pausado" && state !== "todos" && (item.paused || item.status !== state)) return false;

  const q = semAcento(query.trim());
  if (!q) return true;
  return semAcento(item.name).includes(q) || semAcento(item.target ?? "").includes(q);
}

function semAcento(texto: string): string {
  return texto.normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase();
}
