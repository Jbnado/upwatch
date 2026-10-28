import { useRef, type KeyboardEvent, type ReactNode } from "react";

/**
 * Tabs é uma fileira de abas com a semântica de abas.
 *
 * role="tablist", foco que anda pelas setas e só a aba ativa no Tab: é o
 * que um leitor de tela anuncia como "aba 1 de 3" e o que quem usa o
 * teclado espera — percorrer as abas com Tab, uma a uma, obrigaria a
 * passar por todas para chegar ao conteúdo.
 */
export function Tabs<K extends string>({
  id,
  tabs,
  value,
  onChange,
  label,
}: {
  /** Prefixo dos ids, para ligar cada aba ao seu painel. */
  id: string;
  tabs: { key: K; label: ReactNode }[];
  value: K;
  onChange: (key: K) => void;
  label: string;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);

  function teclar(e: KeyboardEvent, i: number) {
    const ultimo = tabs.length - 1;
    const destino =
      e.key === "ArrowRight"
        ? (i === ultimo ? 0 : i + 1)
        : e.key === "ArrowLeft"
          ? (i === 0 ? ultimo : i - 1)
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? ultimo
              : null;
    if (destino === null) return;

    e.preventDefault();
    refs.current[destino]?.focus();
    onChange(tabs[destino]!.key);
  }

  return (
    <div role="tablist" aria-label={label} className="flex gap-4 border-b border-line">
      {tabs.map((t, i) => {
        const ativa = t.key === value;
        return (
          <button
            key={t.key}
            ref={(el) => {
              refs.current[i] = el;
            }}
            type="button"
            role="tab"
            id={`${id}-aba-${t.key}`}
            aria-selected={ativa}
            aria-controls={`${id}-painel-${t.key}`}
            tabIndex={ativa ? 0 : -1}
            onClick={() => onChange(t.key)}
            onKeyDown={(e) => teclar(e, i)}
            className={[
              // A marca da aba ativa é traço e peso, não cor: cor aqui
              // significa estado do alvo.
              "pressable -mb-px border-b-2 px-0.5 pb-2 text-body",
              ativa
                ? "border-ink font-medium text-ink"
                : "border-transparent text-ink-3 hover:text-ink active:text-ink-2",
            ].join(" ")}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}

/** TabPanel é o conteúdo de uma aba, ligado a ela pelo id. */
export function TabPanel({
  id,
  tab,
  children,
}: {
  id: string;
  tab: string;
  children: ReactNode;
}) {
  return (
    <div
      role="tabpanel"
      id={`${id}-painel-${tab}`}
      aria-labelledby={`${id}-aba-${tab}`}
      className="flex flex-col gap-4 pt-4"
    >
      {children}
    </div>
  );
}
