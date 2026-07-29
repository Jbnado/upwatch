import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";
import type { Status } from "../api/types";

/**
 * Primitivas da interface.
 *
 * Escritas à mão em vez de trazidas de uma biblioteca: são poucas, e o
 * conjunto pronto viria com o visual de todo mundo — que é exatamente o
 * que este projeto não quer.
 *
 * Nada aqui é colorido. Cor é reservada a estado, então botão, campo e
 * borda vivem na escala de tinta e a única cor da tela sempre quer dizer
 * alguma coisa.
 */

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "ghost" | "danger";
};

export function Button({ variant = "ghost", className = "", ...props }: ButtonProps) {
  const base =
    "inline-flex h-8 items-center justify-center gap-1.5 rounded-[3px] px-3 " +
    "text-[13px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-45";

  const variants = {
    primary: "bg-ink text-paper hover:bg-ink-2",
    ghost: "border border-line-strong text-ink hover:bg-sunken",
    // Ação destrutiva é o único caso em que a cor não descreve estado do
    // alvo; ainda assim fica contida na borda, sem preencher o botão.
    danger: "border border-down/40 text-down hover:bg-down-dim",
  } as const;

  return <button className={`${base} ${variants[variant]} ${className}`} {...props} />;
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
        <span className="text-[12px] text-down">{error}</span>
      ) : hint ? (
        <span className="text-[12px] text-ink-3">{hint}</span>
      ) : null}
    </label>
  );
}

const controlClass =
  "h-8 w-full rounded-[3px] border border-line-strong bg-surface px-2.5 text-[13px] " +
  "text-ink placeholder:text-ink-3 focus:border-ink focus:outline-none";

export function Input({ className = "", ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input className={`${controlClass} ${className}`} {...props} />;
}

export function Select({ className = "", ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={`${controlClass} ${className}`} {...props} />;
}

/** Campos numéricos usam a monoespaçada tabular, como toda medição. */
export function NumberInput(props: InputHTMLAttributes<HTMLInputElement>) {
  return <Input type="number" inputMode="numeric" className="tabular" {...props} />;
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
      <span className={withLabel ? `text-[13px] ${style.text}` : "sr-only"}>{style.label}</span>
    </span>
  );
}

/**
 * Empty convida à ação em vez de apenas informar vazio.
 */
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
    <div className="flex flex-col items-start gap-3 border border-dashed border-line-strong px-6 py-10">
      <div className="flex flex-col gap-1">
        <p className="text-[15px] font-medium text-ink">{title}</p>
        <p className="max-w-prose text-[13px] text-ink-2">{description}</p>
      </div>
      {action}
    </div>
  );
}

/**
 * Alert mostra falha sem pedir desculpas: o que houve e o que fazer.
 */
export function Alert({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="border-l-2 border-down bg-down-dim px-3 py-2 text-[13px] text-ink"
    >
      {children}
    </div>
  );
}
