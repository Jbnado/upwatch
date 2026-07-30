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
 *
 * O grupo é um só bloco: os botões dividem uma borda externa e se separam
 * por um fio interno, para leitura como controle segmentado e não como
 * sete botões soltos que por acaso ficaram lado a lado.
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
      // Altura de controle, não a reduzida: no cabeçalho do painel ele fica
      // ao lado do botão de ação, e 6px de diferença entre dois controles
      // vizinhos não lê como escolha, lê como desleixo.
      //
      // overflow-hidden para o preenchimento do item selecionado respeitar
      // o arredondamento das pontas do grupo.
      className="inline-flex h-[var(--control-h)] overflow-hidden rounded-sm border border-line-strong"
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
              // Sem rounded no item: o arredondamento é do grupo, e repetir
              // aqui deixaria cantos brancos entre os segmentos.
              "pressable tabular inline-flex items-center px-2.5 text-small",
              i > 0 ? "border-l border-line-strong" : "",
              selecionado
                ? // O selecionado não muda sob o cursor: já está no destino,
                  // e reagir ao hover sugeriria que o clique faria algo.
                  "bg-ink text-paper"
                : "text-ink-2 hover:bg-sunken hover:text-ink active:bg-pressed",
            ].join(" ")}
          >
            {range.label}
          </button>
        );
      })}
    </div>
  );
}
