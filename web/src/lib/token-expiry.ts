import type { TokenExpiryPolicy } from "../api/types";

/**
 * O seletor de validade de um token novo.
 *
 * A regra de verdade mora no servidor (domain.TokenExpiryPolicy); aqui só
 * se oferece o que ele aceita, para a pessoa não escolher um prazo que
 * volta como erro. O teto corta os presets grandes demais e limita a data
 * personalizada; "sem expiração" só aparece quando a instalação não exige
 * validade — o teto não a proíbe, porque ele só limita um prazo escolhido.
 */

export type ValidityChoice = "7d" | "30d" | "90d" | "custom" | "none";

export type ValidityOption = { value: ValidityChoice; label: string };

const DIA = 86400; // segundos

const PRESETS: { value: ValidityChoice; dias: number; label: string }[] = [
  { value: "7d", dias: 7, label: "7 dias" },
  { value: "30d", dias: 30, label: "30 dias" },
  { value: "90d", dias: 90, label: "90 dias" },
];

// validityOptions monta as opções do seletor a partir da política.
export function validityOptions(policy: TokenExpiryPolicy): ValidityOption[] {
  const teto = policy.max_lifetime_seconds;
  const out: ValidityOption[] = [];
  for (const p of PRESETS) {
    if (teto === 0 || p.dias * DIA <= teto) {
      out.push({ value: p.value, label: p.label });
    }
  }
  out.push({ value: "custom", label: "data personalizada" });
  if (!policy.require_expiry) {
    out.push({ value: "none", label: "sem expiração" });
  }
  return out;
}

// defaultChoice escolhe um prazo finito seguro, nunca "sem expiração" por
// omissão: a validade é uma escolha deliberada, e o padrão nudge-ia para o
// prazo em vez de para o eterno.
export function defaultChoice(policy: TokenExpiryPolicy): ValidityChoice {
  const opts = validityOptions(policy);
  for (const preferida of ["30d", "90d", "7d"] as ValidityChoice[]) {
    if (opts.some((o) => o.value === preferida)) {
      return preferida;
    }
  }
  // Nenhum preset cabe no teto: resta a data personalizada.
  return "custom";
}

// maxCustomDate é o maior dia aceito na data personalizada (yyyy-mm-dd), ou
// "" quando não há teto.
export function maxCustomDate(policy: TokenExpiryPolicy, now: Date = new Date()): string {
  if (policy.max_lifetime_seconds === 0) {
    return "";
  }
  return isoDate(new Date(now.getTime() + policy.max_lifetime_seconds * 1000));
}

// expiresAtFor converte a escolha do seletor em expires_at (ISO 8601), ou
// null para "sem expiração". A data personalizada vale até o fim do dia
// escolhido, para um token criado de manhã não vencer ainda hoje.
export function expiresAtFor(
  choice: ValidityChoice,
  customDate: string,
  now: Date = new Date(),
): string | null {
  switch (choice) {
    case "none":
      return null;
    case "custom":
      if (!customDate) {
        return null;
      }
      return new Date(`${customDate}T23:59:59`).toISOString();
    default: {
      const preset = PRESETS.find((p) => p.value === choice);
      const dias = preset ? preset.dias : 30;
      return new Date(now.getTime() + dias * DIA * 1000).toISOString();
    }
  }
}

function isoDate(d: Date): string {
  return d.toISOString().slice(0, 10);
}
