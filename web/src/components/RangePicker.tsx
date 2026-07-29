import { RANGES, type Range } from "../lib/ranges";

/**
 * Seletor de período da faixa.
 *
 * Fica exposto como fileira de botões, não escondido num menu suspenso:
 * trocar a janela é o gesto mais frequente durante uma investigação, e
 * cada clique a mais é um clique feito com o serviço fora do ar.
 *
 * Não há opção de granularidade. A janela decide sozinha de qual camada
 * ler — quem opera pensa em tempo, não em como o dado foi guardado.
 */
export function RangePicker({
  value,
  onChange,
}: {
  value: Range;
  onChange: (range: Range) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Período exibido"
      className="inline-flex rounded-[3px] border border-line-strong"
    >
      {RANGES.map((range, i) => {
        const selecionado = range.key === value.key;

        return (
          <button
            key={range.key}
            type="button"
            aria-pressed={selecionado}
            onClick={() => onChange(range)}
            className={[
              "tabular px-2.5 py-1 text-[12px] transition-colors",
              i > 0 ? "border-l border-line-strong" : "",
              selecionado ? "bg-ink text-paper" : "text-ink-2 hover:bg-sunken hover:text-ink",
            ].join(" ")}
          >
            {range.label}
          </button>
        );
      })}
    </div>
  );
}
