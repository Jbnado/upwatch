import type {
  AnchorHTMLAttributes,
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  MouseEvent,
  ReactNode,
  SelectHTMLAttributes,
} from "react";
import type { Status } from "../api/types";
import { href, navigate, type Route } from "../lib/router";

/**
 * Primitivas da interface.
 *
 * Escritas à mão em vez de trazidas de uma biblioteca: são poucas, e o
 * conjunto pronto viria com o visual de todo mundo — que é exatamente o
 * que este projeto não quer.
 *
 * Nada aqui é colorido. Cor é reservada a estado, então botão, campo e
 * borda vivem na escala de tinta e a única cor da tela sempre quer dizer
 * alguma coisa. A exceção é a ação destrutiva, e mesmo ela fica contida
 * na borda em vez de preencher o botão.
 *
 * Todo elemento clicável tem os cinco estados: parado, sob o cursor,
 * pressionado, com foco de teclado e desabilitado. Faltar o de
 * pressionado faz o controle parecer travado no instante do clique.
 */

const CONTROL_BASE =
  "pressable inline-flex items-center justify-center gap-1.5 rounded-sm px-3 " +
  "text-body font-medium select-none";

const VARIANTS = {
  primary: "bg-ink text-paper hover:bg-ink-2 active:bg-ink",
  ghost: "border border-line-strong text-ink hover:bg-sunken active:bg-pressed",
  // Ação destrutiva é o único caso em que a cor não descreve estado do
  // alvo; ainda assim fica contida na borda, sem preencher o botão.
  danger: "border border-down/40 text-down hover:bg-down-dim active:bg-down-dim active:border-down",
} as const;

type Variant = keyof typeof VARIANTS;

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: Variant;
  size?: "md" | "sm";
};

/**
 * Button, em dois tamanhos.
 *
 * `md` para ação de cabeçalho e de formulário, `sm` para ação dentro de
 * linha de lista. São duas alturas e não uma porque um botão de 32px numa
 * linha de 40px estica a lista inteira e desmancha a densidade que faz o
 * painel valer a pena.
 */
export function Button({ variant = "ghost", size = "md", className = "", ...props }: ButtonProps) {
  const height = size === "sm" ? "h-[var(--control-h-sm)]" : "h-[var(--control-h)]";

  return <button className={`${CONTROL_BASE} ${height} ${VARIANTS[variant]} ${className}`} {...props} />;
}

/**
 * LinkButton parece botão e é link.
 *
 * Usado onde a ação navega ou baixa: só o link dá abrir em nova aba com
 * Ctrl e copiar endereço com o botão direito, que é o que se espera de
 * qualquer coisa que leve a outro lugar.
 */
export function LinkButton({
  variant = "ghost",
  className = "",
  ...props
}: AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: Variant }) {
  return (
    <a
      className={`${CONTROL_BASE} h-[var(--control-h)] ${VARIANTS[variant]} ${className}`}
      {...props}
    />
  );
}

/**
 * RowLink é uma linha inteira clicável.
 *
 * Precisa ser âncora, não botão, por dois motivos. O primeiro é técnico:
 * button só aceita conteúdo de frase, então uma div dentro dele faz o
 * parser fechar o botão antes da hora e o conteúdo vira irmão — cliques
 * naquela parte deixam de funcionar sem que nada indique o motivo. O
 * segundo é de uso: numa lista de alvos, abrir um em nova aba com Ctrl é
 * expectativa básica, e só link oferece isso.
 */
export function RowLink({
  to,
  className = "",
  children,
}: {
  to: Route;
  className?: string;
  children: ReactNode;
}) {
  return (
    <a
      href={href(to)}
      onClick={interceptar(to)}
      className={`pressable block hover:bg-sunken active:bg-pressed ${className}`}
    >
      {children}
    </a>
  );
}

/**
 * InlineLink é link dentro de frase.
 *
 * Existe para o texto corrido não precisar de âncora crua: sem passar
 * pelo roteador, clicar recarregava a aplicação inteira — a interface
 * inteira baixada de novo para trocar de tela.
 */
export function InlineLink({ to, children }: { to: Route; children: ReactNode }) {
  return (
    <a
      href={href(to)}
      onClick={interceptar(to)}
      className="pressable underline decoration-line-strong hover:decoration-ink active:text-ink-2"
    >
      {children}
    </a>
  );
}

/**
 * interceptar troca a navegação do navegador pela do roteador.
 *
 * Só quando o clique é simples: com Ctrl, Shift, Alt ou botão do meio a
 * pessoa está pedindo nova aba ou nova janela, e o navegador faz isso
 * melhor do que qualquer código nosso faria.
 */
function interceptar(to: Route) {
  return (e: MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
    e.preventDefault();
    navigate(to);
  };
}

/**
 * TextLink é ação em texto — voltar, ir para ajustes, sair.
 *
 * Sublinhado apenas sob o cursor: numa interface densa, sublinhado
 * permanente em todo lugar viraria ruído visual.
 */
export function TextLink({
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={`pressable eyebrow hover:text-ink hover:underline active:text-ink-2 ${className}`}
      {...props}
    />
  );
}

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="eyebrow">{label}</span>
      {children}
      {/* O erro substitui a dica em vez de empilhar: duas linhas de texto
          auxiliar competindo tornam a correção mais lenta. */}
      {error ? (
        <span className="text-small text-down">{error}</span>
      ) : hint ? (
        <span className="text-small text-ink-3">{hint}</span>
      ) : null}
    </label>
  );
}

const CONTROL_FIELD =
  "h-[var(--control-h)] w-full rounded-sm border border-line-strong bg-surface px-2.5 " +
  "text-body text-ink transition-colors placeholder:text-ink-3 " +
  "hover:border-ink-3 focus:border-ink focus:outline-none " +
  "disabled:cursor-not-allowed disabled:opacity-45";

export function Input({ className = "", ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={`${CONTROL_FIELD} ${className}`} {...props} />;
}

export function Select({ className = "", ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={`${CONTROL_FIELD} cursor-pointer ${className}`} {...props} />;
}

/** Campos numéricos usam a monoespaçada tabular, como toda medição. */
export function NumberInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <Input type="number" inputMode="numeric" className="tabular" {...props} />;
}

/**
 * Checkbox com a área de clique estendida ao rótulo inteiro.
 *
 * Alvo de dez pixels exige mira; a linha toda não.
 */
export function Checkbox({
  checked,
  onChange,
  children,
}: {
  checked: boolean;
  onChange: () => void;
  children: ReactNode;
}) {
  return (
    <label className="pressable flex cursor-pointer items-center gap-2 rounded-sm px-1 py-1 text-body hover:bg-sunken active:bg-pressed">
      <input type="checkbox" checked={checked} onChange={onChange} className="accent-ink" />
      {children}
    </label>
  );
}

const STATUS_STYLE: Record<Status, { dot: string; text: string; label: string }> = {
  up: { dot: "bg-up", text: "text-up", label: "no ar" },
  degraded: { dot: "bg-degraded", text: "text-degraded", label: "degradado" },
  down: { dot: "bg-down", text: "text-down", label: "fora do ar" },
  unknown: { dot: "bg-unknown", text: "text-unknown", label: "sem medição" },
};

/**
 * StatusDot marca o estado.
 *
 * Ponto mais rótulo, nunca cor sozinha: cerca de um em cada doze homens
 * não distingue vermelho de verde, e um painel que só usa cor deixa essa
 * pessoa sem informação nenhuma.
 */
export function StatusDot({ status, withLabel = false }: { status: Status; withLabel?: boolean }) {
  const style = STATUS_STYLE[status];

  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${style.dot}`} aria-hidden />
      <span className={withLabel ? `text-body ${style.text}` : "sr-only"}>{style.label}</span>
    </span>
  );
}

/**
 * Nothing é o vazio de seção.
 *
 * Empty desenha uma moldura tracejada grande porque é o convite central de
 * uma tela sem nada. Dentro de uma tela que já tem conteúdo, a mesma
 * moldura grita: "nenhuma queda confirmada" é boa notícia, e boa notícia
 * não merece ser o maior elemento da página.
 */
export function Nothing({ children, hint }: { children: ReactNode; hint?: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <p className="text-body text-ink-2">{children}</p>
      {hint && <p className="max-w-prose text-small text-ink-3">{hint}</p>}
    </div>
  );
}

/** Empty convida à ação em vez de apenas informar vazio. */
export function Empty({
  title,
  description,
  action,
}: {
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-start gap-3 rounded-sm border border-dashed border-line-strong px-6 py-10">
      <div className="flex flex-col gap-1.5">
        <p className="text-lead font-medium text-ink">{title}</p>
        <p className="max-w-prose text-body text-ink-2">{description}</p>
      </div>
      {action}
    </div>
  );
}

/** Alert mostra falha sem pedir desculpas: o que houve e o que fazer. */
export function Alert({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="rounded-sm border-l-2 border-down bg-down-dim px-3 py-2 text-body text-ink"
    >
      {children}
    </div>
  );
}

/** Loading diz o que está carregando, não apenas que algo carrega. */
export function Loading({ what }: { what: string }) {
  return (
    <p className="eyebrow" role="status">
      carregando {what}
    </p>
  );
}
