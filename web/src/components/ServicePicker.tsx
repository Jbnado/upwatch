import { useMemo, useState, type ReactNode } from "react";
import type { Group, Monitor } from "../api/types";
import {
  buildServiceTree,
  filterServiceTree,
  isCollapsible,
  leafIds,
  nodeState,
  openByDefault,
  toggleNode,
  type ServiceNode,
} from "../lib/service-tree";
import { CheckTree, type TreeItem } from "./CheckTree";
import { Input } from "./ui";

/**
 * ServicePicker escolhe monitores pela árvore Sistema › Ambiente ›
 * serviço, com busca.
 *
 * A busca fica acima da árvore e não num filtro à parte: com quarenta
 * serviços, achar "o erp de produção" digitando "erp prod" é mais rápido
 * do que abrir ambiente por ambiente.
 */
export function ServicePicker({
  groups,
  monitors,
  selected,
  onChange,
  renderDetail,
  disabled = false,
}: {
  groups: Group[];
  monitors: Monitor[];
  selected: Set<number>;
  onChange: (next: Set<number>) => void;
  /** Conteúdo sob cada serviço marcado. */
  renderDetail?: (monitorId: number) => ReactNode;
  disabled?: boolean;
}) {
  const [busca, setBusca] = useState("");
  const arvore = useMemo(() => buildServiceTree(groups, monitors), [groups, monitors]);
  const visivel = useMemo(() => filterServiceTree(arvore, busca), [arvore, busca]);

  // Abertos na primeira vez em que há dados: os ambientes com algo
  // marcado. Depois, quem manda é a pessoa — marcar um serviço não
  // deveria abrir nem fechar nada sozinho.
  const [abertos, setAbertos] = useState<Set<string> | null>(null);
  const abertosAgora = abertos ?? openByDefault(arvore, selected);

  const buscando = busca.trim() !== "";

  const porChave = useMemo(() => {
    const mapa = new Map<string, ServiceNode>();
    const visitar = (n: ServiceNode) => {
      mapa.set(n.key, n);
      n.children.forEach(visitar);
    };
    arvore.forEach(visitar);
    return mapa;
  }, [arvore]);

  const item = (n: ServiceNode, pai: ServiceNode | null): TreeItem => {
    const servico = n.monitorId !== undefined;
    const state = nodeState(n, selected);

    if (servico) {
      return {
        key: n.key,
        label: n.name,
        context: n.path,
        state,
        disabled,
        detail: state === "checked" && renderDetail ? renderDetail(n.monitorId!) : undefined,
      };
    }

    const total = leafIds(n).length;
    const marcados = leafIds(n).filter((id) => selected.has(id)).length;
    return {
      key: n.key,
      label: n.name,
      context: pai ? pai.name : undefined,
      meta: (
        <span className="tabular">
          {marcados === 0
            ? `${total} ${total === 1 ? "serviço" : "serviços"}`
            : `${marcados} de ${total}`}
        </span>
      ),
      state,
      disabled,
      strong: pai === null,
      collapsible: isCollapsible(n),
      children: n.children.map((c) => item(c, n)),
    };
  };

  const itens = visivel.map((n) => item(n, null));

  // Na busca tudo abre: o resultado escondido atrás de um ambiente
  // fechado seria um resultado que não apareceu.
  const abertosVisiveis = buscando
    ? new Set(itens.flatMap(function chaves(i): string[] {
        return [i.key, ...(i.children ?? []).flatMap(chaves)];
      }))
    : abertosAgora;

  return (
    <div className="flex flex-col gap-2">
      <Input
        type="search"
        value={busca}
        onChange={(e) => setBusca(e.target.value)}
        placeholder="Buscar por serviço ou grupo — erp prod"
        aria-label="Buscar serviço por nome ou grupo"
      />

      {arvore.length === 0 ? (
        <p className="text-small text-ink-3">Nenhum monitor cadastrado ainda.</p>
      ) : itens.length === 0 ? (
        <p className="text-small text-ink-3">Nenhum serviço com "{busca.trim()}".</p>
      ) : (
        <CheckTree
          items={itens}
          open={abertosVisiveis}
          onOpenChange={(key, abrir) => {
            const proximo = new Set(abertosAgora);
            if (abrir) proximo.add(key);
            else proximo.delete(key);
            setAbertos(proximo);
          }}
          onToggle={(key) => {
            const no = porChave.get(key);
            if (!no) return;
            // Na busca, o grupo marca só o que a busca deixou à vista:
            // marcar "Retaguarda" depois de buscar "erp" e levar junto o
            // fiscal e o banco seria marcar o que não se estava vendo.
            const visto = buscando ? acharVisivel(visivel, key) ?? no : no;
            onChange(toggleNode(visto, selected));
          }}
        />
      )}
    </div>
  );
}

function acharVisivel(nodes: ServiceNode[], key: string): ServiceNode | undefined {
  for (const n of nodes) {
    if (n.key === key) return n;
    const achado = acharVisivel(n.children, key);
    if (achado) return achado;
  }
  return undefined;
}
