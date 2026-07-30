/** Tipos do contrato da API, espelhando internal/api/openapi.yaml. */

export type Status = "up" | "down" | "degraded" | "unknown";

export type MonitorType = "http" | "tcp" | "icmp" | "dns" | "tls" | "push";

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
  parent_id?: number;
  enabled: boolean;
  tags: string[];
  created_at: string;
  updated_at: string;
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

export type User = {
  id: number;
  username: string;
  created_at: string;
  updated_at: string;
};

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

export type ChannelType = "webhook" | "discord" | "slack";

/**
 * A configuração nunca vem do servidor: ela contém a URL do webhook, que
 * é a credencial de quem pode publicar no canal.
 */
export type Channel = {
  id: number;
  name: string;
  type: ChannelType;
  enabled: boolean;
  created_at: string;
  updated_at: string;
};

export type ChannelInput = {
  name: string;
  type: ChannelType;
  enabled?: boolean;
  config: {
    url: string;
    headers?: Record<string, string>;
    template?: string;
  };
};

export type Incident = {
  id: number;
  monitor_id: number;
  started_at: string;
  resolved_at?: string;
  duration_seconds: number;
  cause?: string;
  open: boolean;
};

export type Page<T> = {
  items: T[];
  has_more: boolean;
  next_after_id?: number;
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
