import { useEffect, useState } from "react";

/**
 * Roteador mínimo sobre a History API.
 *
 * Uma biblioteca de rotas traria mais peso do que este punhado de telas
 * justifica, e o binário precisa carregar a interface inteira.
 */

export type Route =
  | { name: "board" }
  | { name: "monitor"; id: number }
  | { name: "monitor-new" }
  | { name: "monitor-edit"; id: number }
  | { name: "settings" }
  | { name: "status-pages" }
  | { name: "status-page"; id: number }
  /**
   * A página pública.
   *
   * É a única rota resolvida antes de qualquer verificação de sessão:
   * quem abre este link não tem conta, e mostrar-lhe uma tela de login
   * anularia o propósito de existir um endereço para compartilhar.
   */
  | { name: "status"; slug?: string };

export function parseRoute(pathname: string): Route {
  const partes = pathname.replace(/^\/+|\/+$/g, "").split("/");

  if (partes[0] === "" || partes[0] === undefined) return { name: "board" };
  if (partes[0] === "settings") return { name: "settings" };

  // "/status" sem slug abre a página padrão: numa instalação com uma
  // página só, o slug repetiria o que o caminho já diz.
  if (partes[0] === "status") {
    return partes[1] ? { name: "status", slug: partes[1] } : { name: "status" };
  }

  if (partes[0] === "status-pages") {
    if (partes[1] === undefined || partes[1] === "") return { name: "status-pages" };

    const id = Number(partes[1]);
    if (Number.isFinite(id) && id > 0) return { name: "status-page", id };
    return { name: "status-pages" };
  }

  if (partes[0] === "monitors") {
    if (partes[1] === "new") return { name: "monitor-new" };

    const id = Number(partes[1]);
    if (Number.isFinite(id) && id > 0) {
      return partes[2] === "edit" ? { name: "monitor-edit", id } : { name: "monitor", id };
    }
  }
  return { name: "board" };
}

export function href(route: Route): string {
  switch (route.name) {
    case "board":
      return "/";
    case "settings":
      return "/settings";
    case "monitor-new":
      return "/monitors/new";
    case "monitor":
      return `/monitors/${route.id}`;
    case "monitor-edit":
      return `/monitors/${route.id}/edit`;
    case "status-pages":
      return "/status-pages";
    case "status-page":
      return `/status-pages/${route.id}`;
    case "status":
      return route.slug ? `/status/${route.slug}` : "/status";
  }
}

export function navigate(route: Route) {
  window.history.pushState({}, "", href(route));
  window.dispatchEvent(new PopStateEvent("popstate"));
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.pathname));

  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.pathname));
    window.addEventListener("popstate", onChange);
    return () => window.removeEventListener("popstate", onChange);
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
