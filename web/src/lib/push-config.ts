/**
 * Configuração do monitor push, entre o formulário e a API.
 *
 * Sem agenda o sinal é esperado por intervalo. Com agenda (cron de 5
 * campos, num fuso explícito) o monitor só cai quando falta o sinal de uma
 * execução prevista — o que serve para rotina de horário fixo, em que
 * nenhum intervalo único funciona.
 */

export type AgendaPush = {
  /** Expressão cron de 5 campos; vazia mantém a espera por intervalo. */
  schedule: string;
  /** Fuso IANA em que a agenda é lida. */
  timezone: string;
  /** Folga em segundos; zero usa o próprio intervalo. */
  folga: number;
};

/** lerPush extrai do monitor salvo o que o formulário edita. */
export function lerPush(
  config: Record<string, unknown> | undefined,
  fusoPadrao: string,
): { token: string; agenda: AgendaPush } {
  const c = config ?? {};
  const texto = (v: unknown) => (typeof v === "string" ? v : "");

  return {
    token: texto(c["token"]),
    agenda: {
      schedule: texto(c["schedule"]),
      timezone: texto(c["timezone"]) || fusoPadrao,
      folga: typeof c["grace_period_seconds"] === "number" ? c["grace_period_seconds"] : 0,
    },
  };
}

/**
 * montarPushConfig produz a configuração enviada ao servidor.
 *
 * O fuso só vai junto da agenda: sozinho não significa nada, e guardá-lo
 * faria parecer que o monitor segue um horário que ele não segue.
 */
export function montarPushConfig(token: string, agenda: AgendaPush): Record<string, unknown> {
  const config: Record<string, unknown> = { token };
  const schedule = agenda.schedule.trim();

  if (schedule) {
    config["schedule"] = schedule;
    config["timezone"] = agenda.timezone.trim();
  }
  if (agenda.folga > 0) config["grace_period_seconds"] = agenda.folga;

  return config;
}

/**
 * proximaExecucao formata a execução prevista no fuso do monitor.
 *
 * O fuso do monitor, e não o do navegador: é o horário escrito no
 * crontab, e é comparando os dois que um fuso errado aparece.
 */
export function proximaExecucao(iso: string, timezone: string): string {
  const quando = new Date(iso);
  const formatar = (tz: string) =>
    new Intl.DateTimeFormat("pt-BR", {
      timeZone: tz,
      day: "2-digit",
      month: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    })
      .format(quando)
      .replace(",", "");

  try {
    return `${formatar(timezone)} (${timezone})`;
  } catch {
    return `${formatar("UTC")} (UTC)`;
  }
}

/** fusoLocal é o fuso de quem está usando a interface. */
export function fusoLocal(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
