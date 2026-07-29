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
  | { name: "settings" };

export function parseRoute(pathname: string): Route {
  const partes = pathname.replace(/^\/+|\/+$/g, "").split("/");

  if (partes[0] === "" || partes[0] === undefined) return { name: "board" };
  if (partes[0] === "settings") return { name: "settings" };

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
