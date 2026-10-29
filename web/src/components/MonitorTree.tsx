import { useMemo, useState, type ReactNode } from "react";
import type { Group, Monitor } from "../api/types";
import {
  buildServiceTree,
  filterServiceTree,
  isCollapsible,
  leafIds,
  nodeState,
  openByDefault,
  subtreeIds,
  toggleNode,
  type ServiceNode,
} from "../lib/service-tree";
import { CheckTree, type TreeItem } from "./CheckTree";
import { InlineLink, Input } from "./ui";

/** Caixa que o clique não muda, com o porquê ao lado. */
export type LockedBox = { checked: boolean; reason: string };

/**
 * MonitorTree mostra e escolhe monitores pela árvore Sistema › Ambiente ›
 * serviço › dependentes, com busca.
 *
 * É a árvore de toda escolha de monitores — página pública, relato,
 * ligar dependentes — para que cada tela não ensine um jeito diferente
 * de ler a mesma hierarquia. Com selected e onChange, ganha caixas: o
 * grupo marca todos os monitores de dentro, e fica parcial quando só
 * parte está marcada. Sem eles, é só a hierarquia, com cada monitor
 * levando à página dele.
 *
 * A busca fica acima da árvore e não num filtro à parte: com quarenta
 * serviços, achar "o erp de produção" digitando "erp prod" é mais rápido
 * do que abrir ambiente por ambiente.
 */
export function MonitorTree({
  groups,
  monitors,
  selected,
  onChange,
  locked,
  describe,
  note,
  renderDetail,
  disabled = false,
  search = true,
}: {
  groups: Group[];
  monitors: Monitor[];
  /** Os monitores marcados. Com onChange, a árvore ganha caixas. */
  selected?: Set<number>;
  onChange?: (next: Set<number>) => void;
  /**
   * Monitores com caixa fixa, que nem o clique nem o grupo mudam, como
   * quem já depende deste ou o próprio monitor.
   */
  locked?: Map<number, LockedBox>;
  /**
   * Texto ao lado do monitor. Sem ele, o monitor cujo pai não está logo
   * acima diz de quem depende.
   */
  describe?: (m: Monitor) => ReactNode;
  /** Aviso junto das caixas, como o que marcar um grupo grava. */
  note?: ReactNode;
  /** Conteúdo sob cada serviço marcado. */
  renderDetail?: (monitorId: number) => ReactNode;
  disabled?: boolean;
  search?: boolean;
}) {
  const [busca, setBusca] = useState("");
  const arvore = useMemo(() => buildServiceTree(groups, monitors), [groups, monitors]);
  const visivel = useMemo(() => filterServiceTree(arvore, busca), [arvore, busca]);

  const marcavel = selected !== undefined && onChange !== undefined;
  const marcados = selected ?? new Set<number>();
  const travados = useMemo(() => new Set(locked?.keys() ?? []), [locked]);

  // Abertos na primeira vez em que há dados: os ambientes com algo
  // marcado. Depois, quem manda é a pessoa — marcar um serviço não
  // deveria abrir nem fechar nada sozinho. Sem caixas, tudo começa
  // aberto: a árvore é para ler.
  const [abertos, setAbertos] = useState<Set<string> | null>(null);
  const abertosAgora = abertos ?? (marcavel ? openByDefault(arvore, marcados) : todasAsChaves(arvore));

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

  const nomeDe = useMemo(() => new Map(monitors.map((m) => [m.id, m.name])), [monitors]);

  const meta = (m: Monitor, pai: ServiceNode | null): ReactNode => {
    const fixo = locked?.get(m.id);
    if (fixo) return fixo.reason;
    if (describe) return describe(m);
    // Sob o pai a árvore já diz de quem ele depende.
    if (m.parent_id == null || pai?.monitorId === m.parent_id) return undefined;
    const nome = nomeDe.get(m.parent_id);
    return nome ? `depende de ${nome}` : undefined;
  };

  const item = (n: ServiceNode, pai: ServiceNode | null): TreeItem => {
    const filhos = n.children.map((c) => item(c, n));

    if (n.monitorId !== undefined) {
      const fixo = locked?.get(n.monitorId);
      const state = !marcavel
        ? undefined
        : fixo
          ? fixo.checked
            ? "checked"
            : "unchecked"
          : nodeState(n, marcados);
      return {
        key: n.key,
        label: marcavel ? n.name : <InlineLink to={{ name: "monitor", id: n.monitorId }}>{n.name}</InlineLink>,
        name: n.name,
        context: n.path,
        meta: meta(n.monitor!, pai),
        state,
        disabled: disabled || fixo !== undefined,
        collapsible: isCollapsible(n),
        children: filhos,
        detail: state === "checked" && !fixo && renderDetail ? renderDetail(n.monitorId) : undefined,
      };
    }

    const escolhiveis = leafIds(n, travados);
    const quantos = escolhiveis.filter((id) => marcados.has(id)).length;
    const total = marcavel ? escolhiveis.length : subtreeIds(n).length;
    return {
      key: n.key,
      label: n.name,
      context: pai ? pai.name : undefined,
      meta: (
        <span className="tabular">
          {quantos === 0
            ? `${total} ${total === 1 ? "serviço" : "serviços"}`
            : `${quantos} de ${total}`}
        </span>
      ),
      state: marcavel ? nodeState(n, marcados, travados) : undefined,
      // Um grupo só de travados não tem o que marcar.
      disabled: disabled || (marcavel && escolhiveis.length === 0),
      strong: pai === null,
      collapsible: isCollapsible(n),
      children: filhos,
    };
  };

  const itens = visivel.map((n) => item(n, null));

  // Na busca tudo abre: o resultado escondido atrás de um ambiente
  // fechado seria um resultado que não apareceu.
  const abertosVisiveis = buscando ? todasAsChaves(visivel) : abertosAgora;

  return (
    <div className="flex flex-col gap-2">
      {search && (
        <Input
          type="search"
          value={busca}
          onChange={(e) => setBusca(e.target.value)}
          placeholder="Buscar por serviço ou grupo — erp prod"
          aria-label="Buscar serviço por nome ou grupo"
        />
      )}

      {marcavel && (note || buscando) && (
        <div className="flex flex-col gap-0.5 text-small text-ink-3">
          {note && <p>{note}</p>}
          {buscando && <p>Com a busca, marcar um grupo marca só o que aparece abaixo dele.</p>}
        </div>
      )}

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
            if (!no || !onChange) return;
            // Na busca, o grupo marca só o que a busca deixou à vista:
            // marcar "Retaguarda" depois de buscar "erp" e levar junto o
            // fiscal e o banco seria marcar o que não se estava vendo.
            const visto = buscando ? acharVisivel(visivel, key) ?? no : no;
            onChange(toggleNode(visto, marcados, travados));
          }}
        />
      )}
    </div>
  );
}

function todasAsChaves(nodes: ServiceNode[]): Set<string> {
  return new Set(
    nodes.flatMap(function chaves(n): string[] {
      return [n.key, ...n.children.flatMap(chaves)];
    }),
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
