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

export type Page<T> = {
  items: T[];
  has_more: boolean;
  next_after_id?: number;
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
