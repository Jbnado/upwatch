import { useId, type ReactNode } from "react";
import type { CheckState } from "../lib/service-tree";

/**
 * CheckTree é uma árvore de caixas de marcar.
 *
 * Uma só para o acesso por conta (Sistema › Ambiente) e para escolher
 * monitores (Sistema › Ambiente › serviço › dependentes): as escolhas são
 * a mesma conversa — "quais destes?" sobre a mesma hierarquia —, e duas árvores
 * parecidas com detalhes diferentes ensinariam duas maneiras de ler a
 * mesma coisa.
 *
 * Sem estado, o item não tem caixa: a mesma árvore serve para só mostrar
 * a hierarquia, como a lista de quem depende de quem.
 *
 * A árvore só desenha. Quem a usa decide o estado de cada caixa e o que
 * um clique faz, porque é aí que as duas diferem: no acesso, marcar o
 * sistema vale para ambientes futuros; na página pública, não.
 */

export type TreeItem = {
  key: string;
  label: ReactNode;
  /** Nome do item quando o rótulo não é texto, como um link. */
  name?: string;
  /**
   * Completa o nome da caixa para quem não vê a árvore: "erp" sozinho
   * não diz de qual ambiente é. Fica só para o leitor de tela; quem vê
   * já lê o cabeçalho acima.
   */
  context?: string;
  /** Texto discreto ao lado do rótulo: contagem, "pelo sistema". */
  meta?: ReactNode;
  /** Ausente é linha sem caixa, só para ler. */
  state?: CheckState;
  disabled?: boolean;
  /** Recolhível: ganha o botão de abrir e fechar. */
  collapsible?: boolean;
  children?: TreeItem[];
  /** Conteúdo sob a linha, como o rótulo público do serviço marcado. */
  detail?: ReactNode;
  strong?: boolean;
};

export function CheckTree({
  items,
  onToggle,
  open = new Set(),
  onOpenChange,
}: {
  items: TreeItem[];
  onToggle?: (key: string) => void;
  /** Chaves dos itens recolhíveis abertos. */
  open?: Set<string>;
  onOpenChange?: (key: string, open: boolean) => void;
}) {
  // A coluna do botão de abrir só existe se algum item recolhe: sem isso
  // a árvore do acesso, que não recolhe nada, ganharia um recuo vazio.
  const recolhe = temRecolhivel(items);

  return (
    <ul className="-mx-1 flex flex-col">
      {items.map((item) => (
        <Linha
          key={item.key}
          item={item}
          nivel={0}
          recolhe={recolhe}
          open={open}
          onToggle={onToggle}
          onOpenChange={onOpenChange}
        />
      ))}
    </ul>
  );
}

function temRecolhivel(items: TreeItem[]): boolean {
  return items.some((i) => i.collapsible || temRecolhivel(i.children ?? []));
}

function Linha({
  item,
  nivel,
  recolhe,
  open,
  onToggle,
  onOpenChange,
}: {
  item: TreeItem;
  nivel: number;
  recolhe: boolean;
  open: Set<string>;
  onToggle?: (key: string) => void;
  onOpenChange?: (key: string, open: boolean) => void;
}) {
  const id = useId();
  const marcavel = item.state !== undefined;
  const aberto = !item.collapsible || open.has(item.key);
  const filhos = item.children ?? [];
  const nome = typeof item.label === "string" ? item.label : (item.name ?? "");

  return (
    <li>
      <div className="flex items-start" style={{ paddingLeft: `${nivel * 1.25}rem` }}>
        {recolhe &&
          (item.collapsible ? (
            <button
              type="button"
              aria-expanded={aberto}
              aria-controls={aberto ? id : undefined}
              aria-label={`${aberto ? "Recolher" : "Abrir"} ${nome}${item.context ? `, ${item.context}` : ""}`}
              onClick={() => onOpenChange?.(item.key, !aberto)}
              className="pressable flex h-7 w-6 shrink-0 items-center justify-center rounded-sm text-ink-3 hover:bg-sunken hover:text-ink active:bg-pressed"
            >
              <svg
                aria-hidden
                viewBox="0 0 10 10"
                className={`h-2.5 w-2.5 transition-transform ${aberto ? "rotate-90" : ""}`}
              >
                <path d="M3 1.5 7 5l-4 3.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
              </svg>
            </button>
          ) : (
            <span className="w-6 shrink-0" aria-hidden />
          ))}

        {marcavel ? (
          <label
            className={[
              "pressable flex min-h-7 min-w-0 flex-1 items-center gap-2 rounded-sm px-1 py-1 text-body",
              item.disabled
                ? "cursor-not-allowed text-ink-3"
                : "cursor-pointer hover:bg-sunken active:bg-pressed",
            ].join(" ")}
          >
            <input
              type="checkbox"
              className="shrink-0 accent-ink"
              checked={item.state === "checked"}
              // A caixa parcial é o que mostra, sem abrir o ambiente, que só
              // parte dele foi escolhida.
              ref={(el) => {
                if (el) el.indeterminate = item.state === "mixed";
              }}
              disabled={item.disabled}
              onChange={() => onToggle?.(item.key)}
            />
            <Rotulo item={item} />
          </label>
        ) : (
          <div className="flex min-h-7 min-w-0 flex-1 items-center gap-2 px-1 py-1 text-body">
            <Rotulo item={item} />
          </div>
        )}
      </div>

      {item.detail && (
        <div
          // pr-1 desfaz o -mx-1 da lista: sem ele, o campo do detalhe
          // passava quatro pixels da borda da coluna.
          className="pb-2 pr-1 pt-0.5"
          // Alinha o detalhe com o texto do rótulo, não com a caixa.
          style={{ paddingLeft: `calc(${nivel * 1.25}rem + ${recolhe ? "1.5rem" : "0rem"} + 1.75rem)` }}
        >
          {item.detail}
        </div>
      )}

      {filhos.length > 0 && aberto && (
        <ul id={id} className="flex flex-col">
          {filhos.map((f) => (
            <Linha
              key={f.key}
              item={f}
              nivel={nivel + 1}
              recolhe={recolhe}
              open={open}
              onToggle={onToggle}
              onOpenChange={onOpenChange}
            />
          ))}
        </ul>
      )}
    </li>
  );
}

function Rotulo({ item }: { item: TreeItem }) {
  return (
    <>
      <span className={`min-w-0 truncate ${item.strong ? "font-medium" : ""}`}>{item.label}</span>
      {item.context && <span className="sr-only">, {item.context}</span>}
      {item.meta && <span className="shrink-0 text-small text-ink-3">{item.meta}</span>}
    </>
  );
}
