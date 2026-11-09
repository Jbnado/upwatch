/** Tipos do contrato da API, espelhando internal/api/openapi.yaml. */

import type { ConfigCanal, ConfigEditavel, ConfigEmail, ConfigNtfy } from "../lib/channel-config";

export type Status = "up" | "down" | "degraded" | "unknown";

export type MonitorType =
  | "http"
  | "tcp"
  | "icmp"
  | "dns"
  | "tls"
  | "push"
  | "redis"
  | "postgres"
  | "mysql";

export type Monitor = {
  id: number;
  name: string;
  type: MonitorType;
  target: string;
  interval_seconds: number;
  timeout_seconds: number;
  confirmation_threshold: number;
  degraded_latency_ms: number;
  config?: Record<string, unknown>;
  /**
   * Monitor do qual este depende, como a sonda de quem a verificação
   * depende. Com o pai fora do ar, este não avisa e a falha vira lacuna.
   */
  parent_id?: number;
  /** Grupo folha do monitor; null é "Sem grupo". */
  group_id?: number | null;
  /**
   * Minutos entre lembretes de uma queda aberta e não reconhecida. null
   * herda do grupo; 0 desliga.
   */
  reminder_minutes?: number | null;
  enabled: boolean;
  tags: string[];
  created_at: string;
  updated_at: string;
  /** Próxima execução prevista de um push com agenda; só no detalhe. */
  next_expected_push?: string;
};

export type MonitorInput = {
  name: string;
  type: MonitorType;
  target: string;
  interval_seconds: number;
  timeout_seconds: number;
  confirmation_threshold?: number;
  degraded_latency_ms?: number;
  config?: Record<string, unknown>;
  enabled?: boolean;
  tags?: string[];
  /** O PUT sobrescreve: omitir move o monitor para "Sem grupo". */
  group_id?: number | null;
  /** O PUT sobrescreve: omitir desliga o pai. */
  parent_id?: number | null;
  /** O PUT sobrescreve: omitir volta a herdar. */
  reminder_minutes?: number | null;
};

/**
 * Ancestral em queda que cala o monitor. id nulo e nome ausente quando a
 * conta não enxerga o ancestral: o filho mostra que está calado sem
 * revelar o que existe fora do escopo.
 */
export type AffectedBy = { id: number | null; name?: string };

/**
 * Resumo de uma janela, calculado no servidor.
 *
 * Os numéricos são anuláveis de propósito: null significa que não houve
 * medição, e zero é uma afirmação — "esteve fora o tempo todo", "respondeu
 * instantaneamente". Confundir os dois é a forma mais silenciosa de uma
 * ferramenta de vigilância mentir, e por isso a distinção atravessa a API
 * inteira até aqui.
 */
export type Summary = {
  monitor_id: number;
  /**
   * Estado confirmado: o mesmo do cartão de grupo e de "Problemas agora".
   * Não é o da última batida — uma falha só vira queda depois do limiar.
   */
  status: Status;
  /**
   * Resultado da última batida, sem confirmação; null quando nunca houve.
   * É por ele que a tela vê a falha que ainda não virou queda.
   */
  last_check_status: Status | null;
  source: "raw" | "hourly" | "daily";
  uptime_percent: number | null;
  latency_p50_ms: number | null;
  latency_p95_ms: number | null;
  latency_p99_ms: number | null;
  last_check_at: string | null;
  up: number;
  degraded: number;
  down: number;
  unknown: number;
  series: SummaryPoint[];
  /**
   * Presente quando um ancestral em queda cala o monitor. A tela mostra
   * "sem sinal — pai fora" no lugar do estado: a falha não é dele.
   */
  affected_by?: AffectedBy;
  /** Quem disse estar olhando a queda aberta; ausente sem reconhecimento. */
  acknowledgement?: Acknowledgement;
};

/** O "estou vendo" de alguém sobre uma queda aberta. */
export type Acknowledgement = {
  by: string;
  at: string;
  note?: string;
};

export type SummaryPoint = {
  at: string;
  status: Status;
  latency_ms: number | null;
};

export type Heartbeat = {
  timestamp: string;
  status: Status;
  latency_ms: number;
  probe_id: string;
  message?: string;
};

export type Rollup = {
  bucket_start: string;
  resolution: "hourly" | "daily";
  total: number;
  up: number;
  down: number;
  degraded: number;
  unknown: number;
  uptime_percent: number;
  latency_avg_ms: number;
  latency_min_ms: number;
  latency_max_ms: number;
  latency_p50_ms: number;
  latency_p95_ms: number;
  latency_p99_ms: number;
};

export type Role = "admin" | "viewer";

/**
 * Sobre quais grupos o papel vale. Conceder um sistema concede os
 * ambientes dele. A barreira está no servidor; aqui é só o que mostrar.
 */
export type Scope = {
  all_groups: boolean;
  group_ids: number[];
};

/** Grupo de monitores: Sistema → Ambiente, dois níveis no máximo. */
export type Group = {
  id: number;
  name: string;
  slug: string;
  parent_id: number | null;
  /** Nome completo, como "RPA / prod". */
  path: string;
  /** Lembrete dos monitores que não escolheram o próprio; null herda, 0 desliga. */
  reminder_minutes?: number | null;
  created_at: string;
  updated_at: string;
};

export type GroupInput = {
  name: string;
  slug?: string;
  parent_id?: number;
  /** O PUT sobrescreve: omitir volta a herdar. */
  reminder_minutes?: number | null;
};

/**
 * Estado de um grupo: o pior entre os monitores ativos, ou "empty" para o
 * grupo sem monitor nenhum. "empty" não é "unknown": não há o que medir,
 * e não há problema nenhum.
 */
export type GroupStatus = Status | "empty";

/**
 * Cartão de um grupo na tela inicial. O sistema soma os ambientes dele;
 * group_id nulo é "Sem grupo".
 */
export type GroupSummary = {
  group_id: number | null;
  parent_id: number | null;
  name: string;
  path: string;
  /** Pior estado entre os ativos; "empty" sem monitor nenhum. */
  status: GroupStatus;
  total: number;
  up: number;
  degraded: number;
  down: number;
  unknown: number;
  paused: number;
  /** Calados por um pai em queda: nem problema, nem no ar. */
  affected: number;
  /** down + degraded + unknown, sem os calados. */
  problems: number;
};

/** Uma linha de "Problemas agora". */
export type Problem = {
  monitor_id: number;
  name: string;
  type: MonitorType;
  group_id: number | null;
  /** Vazio é "Sem grupo". */
  group_path: string;
  status: Exclude<Status, "up">;
  /** Desde quando o estado vale; para monitor nunca medido, o cadastro. */
  since: string;
  cause: string;
  last_check_at: string | null;
  /** Monitores calados por esta queda; eles não aparecem na lista. */
  dependents: number;
};

/**
 * Evento da linha do tempo do monitor: mudança confirmada de estado
 * (`state`) ou batida com falha (`failure`).
 */
export type MonitorEvent = {
  type: "state" | "failure";
  at: string;
  status: Status;
  /** Estado anterior; só em `state`. */
  from?: Status;
  message?: string;
  latency_ms?: number;
  probe_id?: string;
};

export type EventPage = {
  items: MonitorEvent[];
  has_more: boolean;
  /** Só com has_more; volta em `cursor` para a página seguinte. Opaco. */
  next_cursor?: string;
};

export type User = {
  id: number;
  username: string;
  /** admin administra; viewer só lê. */
  role: Role;
  scope: Scope;
  /** A conta tem a verificação em duas etapas ligada. */
  totp_enabled: boolean;
  /**
   * Só em /auth/me: a instalação exige o segundo fator desta conta e ele
   * ainda não foi cadastrado. Enquanto for verdadeiro, o servidor recusa
   * tudo que não seja o cadastro.
   */
  two_factor_enrollment_required?: boolean;
  created_at: string;
  updated_at: string;
};

/**
 * LoginResult é a resposta do login. Com o segundo fator ligado, a senha
 * certa ainda não abre sessão: devolve o desafio que vai junto do código.
 */
export type LoginResult =
  | { status: "ok" }
  | { status: "two_factor_required"; challenge: string; expires_at: string };

export type TOTPStatus = {
  enabled: boolean;
  /** Cadastro começado e não confirmado. */
  pending: boolean;
  recovery_codes_left: number;
  /** A instalação exige o segundo fator desta conta. */
  required: boolean;
};

export type TOTPSetup = { secret: string; uri: string };

export type RecoveryCodes = { recovery_codes: string[] };

export type APIToken = {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
};

/** CreatedToken traz o segredo, que só aparece nesta resposta. */
export type CreatedToken = APIToken & { token: string };

export type ChannelType = "webhook" | "discord" | "slack" | "teams" | "email" | "ntfy";

/**
 * A configuração não vem na listagem: ela contém a URL do webhook, a
 * senha do SMTP ou o token do ntfy — credenciais de quem pode publicar no
 * canal. A edição a recebe sem elas (ChannelDetail).
 */
export type Channel = {
  id: number;
  name: string;
  type: ChannelType;
  enabled: boolean;
  created_at: string;
  updated_at: string;
  /** Onde o canal está ligado direto; para conta com escopo, só o dela. */
  targets?: ChannelTargets;
};

/**
 * Grupos e monitores ligados ao canal. Grupo vale para os monitores dele
 * e, no sistema, para os ambientes, inclusive os criados depois.
 */
export type ChannelTargets = { group_ids: number[]; monitor_ids: number[] };

/** O canal com a configuração para editar, que só volta a quem pode salvá-la. */
export type ChannelDetail = Channel & { config?: ConfigEditavel };

export type ChannelInput = {
  name: string;
  type: ChannelType;
  enabled?: boolean;
  config: ConfigCanal | ConfigEmail | ConfigNtfy;
  /** Troca todos os vínculos junto; ausente na edição, mantém os atuais. */
  targets?: ChannelTargets;
};

export type Incident = {
  id: number;
  monitor_id: number;
  started_at: string;
  resolved_at?: string;
  duration_seconds: number;
  cause?: string;
  open: boolean;
  acknowledgement: Acknowledgement | null;
  /** Último lembrete enviado sobre a queda. */
  reminded_at?: string;
};

export type Page<T> = {
  items: T[];
  has_more: boolean;
  next_after_id?: number;
};

/** Página que anda para trás, do mais recente para o mais antigo. */
export type CursorPage<T> = {
  items: T[];
  has_more: boolean;
  /** Só com has_more; volta em `cursor` para a página seguinte. Opaco. */
  next_cursor?: string;
};

// ---------- páginas públicas de estado ----------

export type IncidentImpact = "none" | "minor" | "major" | "critical";
export type IncidentPhase = "investigating" | "identified" | "monitoring" | "resolved";

export type StatusPage = {
  id: number;
  slug: string;
  title: string;
  description?: string;
  show_latency: boolean;
  time_zone?: string;
  enabled: boolean;
  /** Responde em /status, sem slug. No máximo uma. */
  default: boolean;
  created_at: string;
  updated_at: string;
};

export type StatusPageInput = {
  slug: string;
  title: string;
  description?: string;
  show_latency?: boolean;
  time_zone?: string;
  enabled?: boolean;
};

export type StatusPageGroup = {
  id: number;
  page_id: number;
  name: string;
  position: number;
};

export type StatusPageComponent = {
  page_id: number;
  monitor_id: number;
  group_id?: number | null;
  label?: string;
  position: number;
};

export type Announcement = {
  id: number;
  title: string;
  impact: IncidentImpact;
  phase: IncidentPhase;
  global: boolean;
  components?: number[];
  incident_id?: number | null;
  started_at: string;
  resolved_at?: string | null;
  created_at: string;
  updated_at: string;
};

export type AnnouncementUpdate = {
  id: number;
  announcement_id: number;
  phase: IncidentPhase;
  body: string;
  published_at: string;
};

/**
 * O que a página pública devolve.
 *
 * Note o que não existe aqui: endereço de alvo, causa detectada,
 * identificador de monitor. O servidor monta esta resposta num pacote
 * próprio justamente para que o tipo não tenha onde guardá-los.
 */
export type PublicView = {
  slug: string;
  title: string;
  description?: string;
  time_zone?: string;
  status: Status;
  impact: IncidentImpact;
  window_days: number;
  generated_at: string;
  groups: PublicGroup[];
  announcements: PublicAnnouncement[];
  /** Janelas em curso e as que começam nos próximos sete dias. */
  maintenances?: PublicMaintenance[];
};

export type PublicGroup = {
  /** Vazio é o grupo implícito dos componentes sem agrupamento. */
  name: string;
  monitors: PublicMonitor[];
};

export type PublicMonitor = {
  name: string;
  status: Status;
  /** Ausente quando nada foi observado: zero afirmaria queda total. */
  uptime_percent?: number;
  latency_p95_ms?: number;
  history: PublicDay[];
  /** Numa janela de manutenção agora. */
  maintenance?: boolean;
};

export type PublicDay = {
  /** Dia de calendário em AAAA-MM-DD, não instante. */
  date: string;
  status: Status;
  uptime_percent?: number;
};

export type PublicAnnouncement = {
  title: string;
  impact: IncidentImpact;
  phase: IncidentPhase;
  /** Rótulos públicos, nunca identificadores. */
  components?: string[];
  started_at: string;
  resolved_at?: string | null;
  updates: PublicAnnouncementUpdate[];
};

export type Recurrence = "none" | "daily" | "weekly";

export type PublicMaintenance = {
  title: string;
  message?: string;
  /** Ocorrência em curso ou a próxima. */
  starts_at: string;
  ends_at: string;
  recurrence: Recurrence;
  active: boolean;
  /** Rótulos públicos dos componentes afetados. */
  components: string[];
};

/**
 * Janela de manutenção. Dentro dela a falha não avisa nem conta como
 * queda; as verificações continuam.
 */
export type MaintenanceWindow = {
  id: number;
  title: string;
  message?: string;
  /** Primeira ocorrência; a recorrente repete o horário no fuso dela. */
  starts_at: string;
  ends_at: string;
  recurrence: Recurrence;
  time_zone: string;
  monitor_ids: number[];
  group_ids: number[];
  /** Encerramento antes do previsto. */
  ended_at?: string | null;
  state: "scheduled" | "active" | "finished";
  /** Ocorrência em curso ou a próxima, calculada pelo servidor. */
  next?: { start: string; end: string };
};

export type MaintenanceInput = {
  title: string;
  message: string;
  starts_at: string;
  ends_at: string;
  recurrence: Recurrence;
  time_zone: string;
  monitor_ids: number[];
  group_ids: number[];
};

export type PublicAnnouncementUpdate = {
  phase: IncidentPhase;
  body: string;
  published_at: string;
};

/** ApiError carrega o código e o campo reprovado, quando houver. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly field?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }

  /** unauthenticated indica que é preciso entrar de novo. */
  get unauthenticated(): boolean {
    return this.status === 401;
  }
}
