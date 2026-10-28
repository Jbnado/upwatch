import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";

/**
 * Menu é o "⋯" das ações que não se usam todo dia.
 *
 * Exportar e remover saem do cabeçalho porque competiam com a única
 * pergunta da tela — "o que houve com este alvo?". Remover fica por
 * último e em vermelho, longe do dedo.
 *
 * Teclado de menu: setas andam entre os itens, Esc fecha e devolve o foco
 * ao botão, clicar fora fecha.
 */

export type MenuItem =
  | { kind: "action"; label: string; onSelect: () => void; danger?: boolean }
  | { kind: "link"; label: string; href: string };

export function Menu({ label, items }: { label: string; items: MenuItem[] }) {
  const [aberto, setAberto] = useState(false);
  const botao = useRef<HTMLButtonElement>(null);
  const lista = useRef<HTMLDivElement>(null);
  const id = useId();

  useEffect(() => {
    if (!aberto) return;
    lista.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus();

    const fora = (e: MouseEvent) => {
      if (!lista.current?.contains(e.target as Node) && e.target !== botao.current) {
        setAberto(false);
      }
    };
    document.addEventListener("mousedown", fora);
    return () => document.removeEventListener("mousedown", fora);
  }, [aberto]);

  function fechar() {
    setAberto(false);
    botao.current?.focus();
  }

  function teclar(e: KeyboardEvent) {
    const itens = [...(lista.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    const atual = itens.indexOf(document.activeElement as HTMLElement);

    if (e.key === "Escape") {
      e.preventDefault();
      fechar();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      itens[(atual + 1) % itens.length]?.focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      itens[(atual - 1 + itens.length) % itens.length]?.focus();
    } else if (e.key === "Tab") {
      setAberto(false);
    }
  }

  const ITEM =
    "pressable flex w-full items-center px-3 py-1.5 text-left text-body hover:bg-sunken focus:bg-sunken active:bg-pressed";

  return (
    <div className="relative">
      <button
        ref={botao}
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={aberto}
        aria-controls={aberto ? id : undefined}
        onClick={() => setAberto((a) => !a)}
        className="pressable inline-flex h-[var(--control-h)] w-[var(--control-h)] items-center justify-center rounded-sm border border-line-strong text-ink hover:bg-sunken active:bg-pressed"
      >
        <span aria-hidden className="text-lead leading-none">
          ⋯
        </span>
      </button>

      {aberto && (
        <div
          ref={lista}
          id={id}
          role="menu"
          aria-label={label}
          onKeyDown={teclar}
          className="absolute right-0 top-[calc(100%+4px)] z-20 min-w-[200px] rounded-sm border border-line-strong bg-surface py-1 shadow-lg"
        >
          {items.map((item, i) => (
            <MenuEntry key={i} item={item} className={ITEM} onDone={() => setAberto(false)} />
          ))}
        </div>
      )}
    </div>
  );
}

function MenuEntry({
  item,
  className,
  onDone,
}: {
  item: MenuItem;
  className: string;
  onDone: () => void;
}): ReactNode {
  if (item.kind === "link") {
    return (
      <a role="menuitem" tabIndex={-1} href={item.href} className={className} onClick={onDone}>
        {item.label}
      </a>
    );
  }
  return (
    <button
      role="menuitem"
      tabIndex={-1}
      type="button"
      className={`${className} ${item.danger ? "mt-1 border-t border-line pt-2 text-down" : "text-ink"}`}
      onClick={() => {
        onDone();
        item.onSelect();
      }}
    >
      {item.label}
    </button>
  );
}
