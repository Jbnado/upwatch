import type { User } from "../api/types";

/**
 * Seções dos ajustes e quem enxerga cada uma.
 *
 * Uma decisão só, consultada pelo menu e pela rota direta: se os dois
 * decidissem cada um por si, um dia o menu esconderia o que a rota abre,
 * ou ofereceria o que ela recusa.
 */

export type SectionId = "grupos" | "contas" | "canais" | "paginas" | "tokens" | "senha";

export const SECTION_IDS: SectionId[] = ["grupos", "contas", "canais", "paginas", "tokens", "senha"];

/**
 * O que a seção exige, espelhando o servidor:
 * - "conta": qualquer um, mesmo observador (trocar a própria senha);
 * - "admin": administrador, recortado pelo escopo dele;
 * - "total": administrador que alcança todos os grupos. Canal e página
 *   pública valem para todos os grupos, e a escrita deles é recusada a
 *   quem não os alcança.
 */
type Requirement = "conta" | "admin" | "total";

type SectionDef = { id: SectionId; label: string; requires: Requirement };

const MENU: { title: string; items: SectionDef[] }[] = [
  {
    title: "Organização",
    items: [
      { id: "grupos", label: "Grupos", requires: "admin" },
      { id: "contas", label: "Contas e acesso", requires: "admin" },
    ],
  },
  { title: "Avisos", items: [{ id: "canais", label: "Canais", requires: "total" }] },
  { title: "Publicar", items: [{ id: "paginas", label: "Páginas de estado", requires: "total" }] },
  // Token é da própria conta e herda o escopo dela: o servidor o emite a
  // qualquer administrador, restrito ou não.
  { title: "Integrações", items: [{ id: "tokens", label: "Tokens de API", requires: "admin" }] },
  { title: "Minha conta", items: [{ id: "senha", label: "Senha", requires: "conta" }] },
];

export type MenuGroup = { title: string; items: { id: SectionId; label: string }[] };

function meets(u: User, r: Requirement): boolean {
  if (r === "conta") return true;
  if (u.role !== "admin") return false;
  return r === "admin" || u.scope.all_groups;
}

/** settingsMenu é o menu da conta, sem título de grupo vazio. */
export function settingsMenu(u: User): MenuGroup[] {
  return MENU.map((g) => ({
    title: g.title,
    items: g.items.filter((i) => meets(u, i.requires)).map(({ id, label }) => ({ id, label })),
  })).filter((g) => g.items.length > 0);
}

/** sectionLabel é o nome da seção como o menu o escreve. */
export function sectionLabel(id: SectionId): string {
  return MENU.flatMap((g) => g.items).find((i) => i.id === id)!.label;
}

export type Access = { ok: true } | { ok: false; reason: string };

/** sectionAccess diz se a conta abre a seção e, se não, por quê. */
export function sectionAccess(u: User, id: SectionId): Access {
  const def = MENU.flatMap((g) => g.items).find((i) => i.id === id)!;
  if (meets(u, def.requires)) return { ok: true };

  if (u.role !== "admin") {
    return {
      ok: false,
      reason: `Esta seção é de quem administra. Sua conta é de observador: vê os monitores, mas não altera ajustes.`,
    };
  }
  return {
    ok: false,
    reason: `O que se configura aqui vale para todos os grupos, então só quem tem acesso a todos os grupos mexe nesta seção. Seu acesso é restrito a alguns grupos.`,
  };
}

/** firstSection é a seção que abre quando se entra em ajustes sem escolher. */
export function firstSection(u: User): SectionId {
  return settingsMenu(u)[0]!.items[0]!.id;
}
