import { useEffect, useState } from "react";
import { SECTION_IDS, type SectionId } from "./settings-sections";

/**
 * Roteador mínimo sobre a History API.
 *
 * Uma biblioteca de rotas traria mais peso do que este punhado de telas
 * justifica, e o binário precisa carregar a interface inteira.
 */

/** Abas do detalhe do monitor. Eventos é a padrão e não vai no endereço. */
export type MonitorTab = "eventos" | "quedas" | "avisos";

const ABAS: MonitorTab[] = ["eventos", "quedas", "avisos"];

export type Route =
  | { name: "board" }
  /** Lista de um grupo; id nulo é "Sem grupo". */
  | { name: "group"; id: number | null }
  /**
   * Detalhe do monitor. A aba e a falha em destaque moram no endereço
   * para o link colado no chat abrir no mesmo lugar para quem recebe.
   */
  | { name: "monitor"; id: number; tab?: MonitorTab; anchor?: string }
  /** group pré-escolhe o grupo de quem cadastra a partir da tela dele. */
  | { name: "monitor-new"; group?: number }
  | { name: "monitor-edit"; id: number }
  /**
   * Ajustes, uma seção por vez. Sem seção é o menu: no celular ele ocupa
   * a tela; no computador, a primeira seção da conta abre ao lado.
   */
  | { name: "ajustes"; section?: SectionId }
  /** Edição de uma página de estado, dentro da seção de páginas. */
  | { name: "ajustes-pagina"; id: number }
  /**
   * A página pública.
   *
   * É a única rota resolvida antes de qualquer verificação de sessão:
   * quem abre este link não tem conta, e mostrar-lhe uma tela de login
   * anularia o propósito de existir um endereço para compartilhar.
   */
  | { name: "status"; slug?: string };

/** Segmento de "Sem grupo", que não tem id. */
const SEM_GRUPO = "sem-grupo";

export function parseRoute(pathname: string, hash = ""): Route {
  const partes = pathname.replace(/^\/+|\/+$/g, "").split("/");

  if (partes[0] === "" || partes[0] === undefined) return { name: "board" };

  const antigo = legacyRedirect(pathname);
  if (antigo) return parseRoute(antigo);

  if (partes[0] === "ajustes") {
    const secao = SECTION_IDS.find((s) => s === partes[1]);
    if (!secao) return { name: "ajustes" };
    if (secao === "paginas" && partes[2]) {
      const id = Number(partes[2]);
      if (Number.isFinite(id) && id > 0) return { name: "ajustes-pagina", id };
    }
    return { name: "ajustes", section: secao };
  }

  if (partes[0] === "groups") {
    if (partes[1] === SEM_GRUPO) return { name: "group", id: null };
    const id = Number(partes[1]);
    if (Number.isFinite(id) && id > 0) return { name: "group", id };
    return { name: "board" };
  }

  // "/status" sem slug abre a página padrão: numa instalação com uma
  // página só, o slug repetiria o que o caminho já diz.
  if (partes[0] === "status") {
    return partes[1] ? { name: "status", slug: partes[1] } : { name: "status" };
  }

  if (partes[0] === "monitors") {
    if (partes[1] === "new") return { name: "monitor-new" };

    const id = Number(partes[1]);
    if (Number.isFinite(id) && id > 0) {
      if (partes[2] === "edit") return { name: "monitor-edit", id };

      const rota: Route = { name: "monitor", id };
      const aba = ABAS.find((a) => a === partes[2]);
      if (aba && aba !== "eventos") rota.tab = aba;
      const ancora = hash.replace(/^#/, "");
      if (ancora) rota.anchor = ancora;
      return rota;
    }
  }
  return { name: "board" };
}

export function href(route: Route): string {
  switch (route.name) {
    case "board":
      return "/";
    case "ajustes":
      return route.section ? `/ajustes/${route.section}` : "/ajustes";
    case "ajustes-pagina":
      return `/ajustes/paginas/${route.id}`;
    case "monitor-new":
      return route.group ? `/monitors/new?grupo=${route.group}` : "/monitors/new";
    case "group":
      return `/groups/${route.id ?? SEM_GRUPO}`;
    case "monitor": {
      const aba = route.tab && route.tab !== "eventos" ? `/${route.tab}` : "";
      const ancora = route.anchor ? `#${route.anchor}` : "";
      return `/monitors/${route.id}${aba}${ancora}`;
    }
    case "monitor-edit":
      return `/monitors/${route.id}/edit`;
    case "status":
      return route.slug ? `/status/${route.slug}` : "/status";
  }
}

/**
 * legacyRedirect devolve o endereço novo de um endereço antigo dos
 * ajustes, ou null quando o endereço já é atual.
 *
 * Favorito, link colado numa issue e histórico do navegador ainda apontam
 * para /settings e /status-pages; quem os abre precisa cair no lugar
 * equivalente — e ver na barra o endereço novo, para o próximo link que
 * copiar já ser o certo.
 */
export function legacyRedirect(pathname: string): string | null {
  const partes = pathname.replace(/^\/+|\/+$/g, "").split("/");

  if (partes[0] === "settings") return "/ajustes";
  if (partes[0] !== "status-pages") return null;

  const id = Number(partes[1]);
  if (partes[1] && Number.isFinite(id) && id > 0) return `/ajustes/paginas/${id}`;
  return "/ajustes/paginas";
}

/**
 * navigate troca de tela. replace serve à troca de aba: voltar no
 * navegador deveria sair da tela, não desfazer cada aba visitada.
 */
export function navigate(route: Route, { replace = false }: { replace?: boolean } = {}) {
  if (replace) window.history.replaceState({}, "", href(route));
  else window.history.pushState({}, "", href(route));
  window.dispatchEvent(new PopStateEvent("popstate"));
}

/**
 * atual lê a rota da barra de endereço, trocando o endereço antigo pelo
 * novo no lugar — replace, e não push: voltar no navegador não deveria
 * levar de novo ao endereço que acabou de ser corrigido.
 */
function atual(): Route {
  const novo = legacyRedirect(window.location.pathname);
  if (novo) window.history.replaceState({}, "", novo + window.location.search + window.location.hash);
  return parseRoute(window.location.pathname, window.location.hash);
}

export function useRoute(): Route {
  const [route, setRoute] = useState(atual);

  useEffect(() => {
    const onChange = () => setRoute(atual());
    window.addEventListener("popstate", onChange);
    // Trocar só a âncora não dispara popstate em todo navegador.
    window.addEventListener("hashchange", onChange);
    return () => {
      window.removeEventListener("popstate", onChange);
      window.removeEventListener("hashchange", onChange);
    };
  }, []);

  return route;
}

/**
 * pool executa as tarefas com um teto de simultaneidade.
 *
 * O painel busca o histórico de cada monitor em paralelo; sem teto, uma
 * instalação com cem alvos abriria cem requisições de uma vez e o
 * navegador as enfileiraria de qualquer forma, só que sem ordem.
 */
export async function pool<T, R>(
  items: T[],
  limit: number,
  task: (item: T) => Promise<R>,
): Promise<R[]> {
  const results: R[] = new Array(items.length);
  let next = 0;

  async function worker() {
    while (next < items.length) {
      const index = next++;
      results[index] = await task(items[index]!);
    }
  }

  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}
