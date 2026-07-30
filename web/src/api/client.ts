import {
  ApiError,
  type Announcement,
  type AnnouncementUpdate,
  type APIToken,
  type Channel,
  type ChannelInput,
  type ChannelType,
  type CreatedToken,
  type Heartbeat,
  type Incident,
  type IncidentImpact,
  type IncidentPhase,
  type Monitor,
  type MonitorInput,
  type Page,
  type PublicView,
  type Rollup,
  type StatusPage,
  type StatusPageComponent,
  type StatusPageGroup,
  type StatusPageInput,
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

  // ---------- canais de aviso ----------

  // A lista de tipos vem do servidor para a interface não manter uma cópia
  // que sai de sincronia a cada canal novo.
  channelTypes: () => request<{ items: ChannelType[] }>("/channel-types"),

  listChannels: () => request<{ items: Channel[] }>("/channels"),

  createChannel: (input: ChannelInput) =>
    request<Channel>("/channels", { method: "POST", body: JSON.stringify(input) }),

  updateChannel: (id: number, input: ChannelInput) =>
    request<Channel>(`/channels/${id}`, { method: "PUT", body: JSON.stringify(input) }),

  deleteChannel: (id: number) => request<void>(`/channels/${id}`, { method: "DELETE" }),

  // A entrega é síncrona: quem apertou o botão está esperando saber se
  // chegou de verdade.
  testChannel: (id: number) =>
    request<{ status: string }>(`/channels/${id}/test`, { method: "POST" }),

  monitorChannels: (monitorID: number) =>
    request<{ items: Channel[] }>(`/monitors/${monitorID}/channels`),

  linkChannel: (monitorID: number, channelID: number) =>
    request<void>(`/monitors/${monitorID}/channels/${channelID}`, { method: "PUT" }),

  unlinkChannel: (monitorID: number, channelID: number) =>
    request<void>(`/monitors/${monitorID}/channels/${channelID}`, { method: "DELETE" }),

  // ---------- incidentes ----------

  incidents: (params: { monitor_id?: number; open?: string; limit?: number } = {}) =>
    request<Page<Incident>>(`/incidents${query(params)}`),

  // ---------- páginas públicas ----------

  /**
   * publicStatus busca a página sem credencial.
   *
   * É a única chamada da interface que funciona deslogado, e é o ponto
   * inteiro da funcionalidade.
   */
  publicStatus: (slug?: string) =>
    request<PublicView>(slug ? `/public/${encodeURIComponent(slug)}` : "/public"),

  feedUrl: (slug?: string) =>
    slug ? `${BASE}/public/${encodeURIComponent(slug)}/feed.atom` : `${BASE}/public/feed.atom`,

  listStatusPages: () => request<{ items: StatusPage[] }>("/status-pages"),

  getStatusPage: (id: number) =>
    request<{
      page: StatusPage;
      groups: StatusPageGroup[];
      components: StatusPageComponent[];
    }>(`/status-pages/${id}`),

  createStatusPage: (input: StatusPageInput) =>
    request<StatusPage>("/status-pages", { method: "POST", body: JSON.stringify(input) }),

  updateStatusPage: (id: number, input: StatusPageInput) =>
    request<StatusPage>(`/status-pages/${id}`, { method: "PUT", body: JSON.stringify(input) }),

  deleteStatusPage: (id: number) => request<void>(`/status-pages/${id}`, { method: "DELETE" }),

  setDefaultStatusPage: (id: number) =>
    request<StatusPage>(`/status-pages/${id}/default`, { method: "PUT" }),

  createGroup: (pageID: number, input: { name: string; position?: number }) =>
    request<StatusPageGroup>(`/status-pages/${pageID}/groups`, {
      method: "POST",
      body: JSON.stringify(input),
    }),

  deleteGroup: (pageID: number, groupID: number) =>
    request<void>(`/status-pages/${pageID}/groups/${groupID}`, { method: "DELETE" }),

  setComponent: (
    pageID: number,
    monitorID: number,
    input: { group_id?: number | null; label?: string; position?: number },
  ) =>
    request<StatusPageComponent>(`/status-pages/${pageID}/components/${monitorID}`, {
      method: "PUT",
      body: JSON.stringify(input),
    }),

  removeComponent: (pageID: number, monitorID: number) =>
    request<void>(`/status-pages/${pageID}/components/${monitorID}`, { method: "DELETE" }),

  // ---------- relatos ----------

  announcements: (params: { open?: string; limit?: number } = {}) =>
    request<Page<Announcement>>(`/announcements${query(params)}`),

  getAnnouncement: (id: number) =>
    request<{ announcement: Announcement; updates: AnnouncementUpdate[] }>(`/announcements/${id}`),

  createAnnouncement: (input: {
    title: string;
    impact: IncidentImpact;
    phase: IncidentPhase;
    global?: boolean;
    components?: number[];
    body?: string;
  }) => request<Announcement>("/announcements", { method: "POST", body: JSON.stringify(input) }),

  deleteAnnouncement: (id: number) => request<void>(`/announcements/${id}`, { method: "DELETE" }),

  publishUpdate: (id: number, input: { phase: IncidentPhase; body: string }) =>
    request<{ announcement: Announcement; update: AnnouncementUpdate }>(
      `/announcements/${id}/updates`,
      { method: "POST", body: JSON.stringify(input) },
    ),
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
