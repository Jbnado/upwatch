import {
  ApiError,
  type APIToken,
  type CreatedToken,
  type Heartbeat,
  type Monitor,
  type MonitorInput,
  type Page,
  type Rollup,
  type User,
} from "./types";

const BASE = "/api/v1";

/**
 * request faz a chamada e traduz o erro padronizado da API.
 *
 * A tradução acontece num lugar só para que nenhuma tela precise
 * interpretar o corpo do erro por conta própria e invente o próprio jeito
 * de mostrar falha.
 */
async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const resp = await fetch(BASE + path, {
    ...init,
    headers: {
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers,
    },
    // O cookie de sessão precisa acompanhar a requisição.
    credentials: "same-origin",
  });

  if (resp.status === 204) return undefined as T;

  const texto = await resp.text();
  const corpo = texto ? (JSON.parse(texto) as unknown) : null;

  if (!resp.ok) {
    const erro = (corpo as { error?: { code: string; message: string; field?: string } })?.error;
    throw new ApiError(
      resp.status,
      erro?.code ?? "unknown",
      erro?.message ?? `A requisição falhou com status ${resp.status}.`,
      erro?.field,
    );
  }
  return corpo as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [chave, valor] of Object.entries(params)) {
    if (valor !== undefined && valor !== "") search.set(chave, String(valor));
  }
  const texto = search.toString();
  return texto ? `?${texto}` : "";
}

export const api = {
  // ---------- primeiro acesso e sessão ----------

  needsSetup: () => request<{ needs_setup: boolean }>("/setup"),

  createAdmin: (username: string, password: string) =>
    request<User>("/setup", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),

  login: (username: string, password: string) =>
    request<{ status: string }>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),

  logout: () => request<void>("/auth/logout", { method: "POST" }),

  me: () => request<User>("/auth/me"),

  changePassword: (current_password: string, new_password: string) =>
    request<void>("/auth/password", {
      method: "POST",
      body: JSON.stringify({ current_password, new_password }),
    }),

  // ---------- tokens ----------

  listTokens: () => request<{ items: APIToken[] }>("/auth/tokens"),

  createToken: (name: string) =>
    request<CreatedToken>("/auth/tokens", {
      method: "POST",
      body: JSON.stringify({ name }),
    }),

  revokeToken: (id: number) => request<void>(`/auth/tokens/${id}`, { method: "DELETE" }),

  // ---------- monitores ----------

  listMonitors: (params: { limit?: number; after_id?: number } = {}) =>
    request<Page<Monitor>>(`/monitors${query(params)}`),

  getMonitor: (id: number) => request<Monitor>(`/monitors/${id}`),

  createMonitor: (input: MonitorInput) =>
    request<Monitor>("/monitors", { method: "POST", body: JSON.stringify(input) }),

  updateMonitor: (id: number, input: MonitorInput) =>
    request<Monitor>(`/monitors/${id}`, { method: "PUT", body: JSON.stringify(input) }),

  deleteMonitor: (id: number) => request<void>(`/monitors/${id}`, { method: "DELETE" }),

  // ---------- dados ----------

  heartbeats: (id: number, params: { from?: string; to?: string; limit?: number }) =>
    request<{ items: Heartbeat[] }>(`/monitors/${id}/heartbeats${query(params)}`),

  rollups: (
    id: number,
    params: { from?: string; to?: string; resolution?: "hourly" | "daily" },
  ) => request<{ items: Rollup[]; resolution: string }>(`/monitors/${id}/rollups${query(params)}`),

  exportUrl: (id: number, params: { from?: string; to?: string; format: "csv" | "json" }) =>
    `${BASE}/monitors/${id}/export${query(params)}`,
};

/**
 * subscribeEvents ouve o fluxo de mudanças do servidor.
 *
 * Sem isto a interface só mostraria o estado do momento em que a página
 * carregou — e num incidente é exatamente quando ninguém quer apertar
 * atualizar para descobrir o que mudou.
 */
export function subscribeEvents(onEvent: (type: string, data: unknown) => void): () => void {
  const source = new EventSource(`${BASE}/events`);

  const tipos = ["monitor.created", "monitor.updated", "monitor.deleted"];
  for (const tipo of tipos) {
    source.addEventListener(tipo, (e) => {
      onEvent(tipo, JSON.parse((e as MessageEvent).data));
    });
  }

  return () => source.close();
}
