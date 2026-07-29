import { useMemo, useState } from "react";
import { pulseBars, type PulseSample, type PulseStatus } from "./pulse-bars";

/**
 * A faixa de pulso.
 *
 * A barra de heartbeat que virou convenção da categoria só diz se o alvo
 * esteve no ar. Esta codifica duas dimensões no mesmo traço: a cor diz o
 * estado e a altura diz a latência. Assim dá para ver o serviço ficando
 * lento antes de cair, que é a história real de quase todo incidente.
 */

const STATUS_FILL: Record<PulseStatus, string> = {
  up: "bg-up",
  degraded: "bg-degraded",
  down: "bg-down",
  unknown: "bg-unknown",
};

const STATUS_LABEL: Record<PulseStatus, string> = {
  up: "no ar",
  degraded: "degradado",
  down: "fora do ar",
  unknown: "sem medição",
};

type PulseProps = {
  samples: PulseSample[];
  /** Altura da faixa em pixels. */
  height?: number;
  /**
   * Largura máxima de cada barra.
   *
   * As barras crescem para ocupar o espaço disponível, mas param no
   * teto: sem ele, oito amostras virariam oito blocos de cor e a altura
   * — que é a informação — desapareceria.
   */
  maxBarWidth?: number;
  /**
   * Desenha o eixo sob a faixa inteira.
   *
   * Com pouco histórico as barras não chegam à borda esquerda, e sem o
   * eixo o vão vazio parece defeito de layout em vez do que é: tempo
   * que ainda não foi medido.
   */
  baseline?: boolean;
  /** Avisa qual amostra está sob o cursor, ou null ao sair. */
  onInspect?: (sample: PulseSample | null) => void;
  /** Rótulo do período, usado na descrição acessível. */
  rangeLabel?: string;
};

export function Pulse({
  samples,
  height = 34,
  maxBarWidth = 6,
  baseline = false,
  onInspect,
  rangeLabel,
}: PulseProps) {
  const bars = useMemo(() => pulseBars(samples), [samples]);
  const [active, setActive] = useState<number | null>(null);

  const resumo = useMemo(() => summarise(samples, rangeLabel), [samples, rangeLabel]);

  if (bars.length === 0) {
    return (
      <div
        className="flex items-center border border-dashed border-line px-2"
        style={{ height }}
      >
        <span className="eyebrow">sem verificações no período</span>
      </div>
    );
  }

  function inspect(index: number | null) {
    setActive(index);
    onInspect?.(index === null ? null : samples[index]!);
  }

  return (
    <div className="relative" style={{ height }}>
      {baseline && (
        <div className="absolute inset-x-0 bottom-0 h-px bg-line" aria-hidden />
      )}

      <div
        role="img"
        aria-label={resumo}
        // Alinhadas à direita, com o excedente saindo pela esquerda: a
        // linha do tempo cresce a partir de agora, e a amostra mais
        // recente — a que interessa durante um incidente — fica sempre
        // visível na borda.
        className="pulse-ink-in flex h-full items-end justify-end gap-[2px] overflow-hidden"
        onMouseLeave={() => inspect(null)}
      >
        {bars.map((bar, i) => (
          <div
            key={bar.at + i}
            // O título nativo dá a leitura da amostra mesmo onde não há
            // espaço para um mostrador, como nas linhas do painel.
            title={`${new Date(bar.at).toLocaleString("pt-BR")} · ${STATUS_LABEL[bar.status]}${
              bar.status === "up" || bar.status === "degraded" ? ` · ${bar.latencyMs} ms` : ""
            }`}
            className="flex h-full flex-1 items-end"
            style={{ minWidth: 3, maxWidth: maxBarWidth }}
            onMouseEnter={() => inspect(i)}
          >
            <div
              className={[
                STATUS_FILL[bar.status],
                "w-full rounded-[1px] transition-opacity duration-100",
                // Quem está sob o cursor mantém opacidade cheia e o resto
                // recua, para o olho achar a amostra sem perder o contexto.
                active === null || active === i ? "opacity-100" : "opacity-35",
              ].join(" ")}
              style={{ height: `${bar.height * 100}%` }}
            />
          </div>
        ))}
      </div>
    </div>
  );
}

/**
 * summarise descreve a faixa para leitores de tela.
 *
 * Uma imagem de dados sem descrição é uma imagem que só metade das
 * pessoas consegue ler; o resumo carrega a mesma informação que o olho
 * tira de relance.
 */
function summarise(samples: PulseSample[], rangeLabel?: string): string {
  if (samples.length === 0) return "Sem verificações no período.";

  const contagem = samples.reduce<Record<PulseStatus, number>>(
    (acc, s) => ({ ...acc, [s.status]: acc[s.status] + 1 }),
    { up: 0, down: 0, degraded: 0, unknown: 0 },
  );

  const partes = (Object.keys(contagem) as PulseStatus[])
    .filter((status) => contagem[status] > 0)
    .map((status) => `${contagem[status]} ${STATUS_LABEL[status]}`);

  const periodo = rangeLabel ? ` em ${rangeLabel}` : "";
  return `${samples.length} verificações${periodo}: ${partes.join(", ")}.`;
}

export { STATUS_LABEL };
