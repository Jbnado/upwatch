import {
  ApiError,
  type Announcement,
  type AnnouncementUpdate,
  type APIToken,
  type Channel,
  type ChannelInput,
  type ChannelType,
  type CreatedToken,
  type CursorPage,
  type EventPage,
  type Group,
  type GroupInput,
  type GroupSummary,
  type Heartbeat,
  type Incident,
  type IncidentImpact,
  type IncidentPhase,
  type Monitor,
  type MonitorInput,
  type Page,
  type Problem,
  type PublicView,
  type Role,
  type Rollup,
  type Scope,
  type StatusPage,
  type StatusPageComponent,
  type StatusPageGroup,
  type StatusPageInput,
  type Summary as SummaryDTO,
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

  createAdmin: (username: string, password: string, setupToken: string) =>
    request<User>("/setup", {
      method: "POST",
      body: JSON.stringify({ username, password, setup_token: setupToken }),
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

  listMonitors: (params: { limit?: number; after_id?: number; group_id?: number } = {}) =>
    request<Page<Monitor>>(`/monitors${query(params)}`),

  getMonitor: (id: number) => request<Monitor>(`/monitors/${id}`),

  createMonitor: (input: MonitorInput) =>
    request<Monitor>("/monitors", { method: "POST", body: JSON.stringify(input) }),

  updateMonitor: (id: number, input: MonitorInput) =>
    request<Monitor>(`/monitors/${id}`, { method: "PUT", body: JSON.stringify(input) }),

  deleteMonitor: (id: number) => request<void>(`/monitors/${id}`, { method: "DELETE" }),

  /**
   * setChildren põe vários monitores sob este de uma vez — "definir pai"
   * em lote. Tudo ou nada: o servidor recusa o lote inteiro se algum
   * fechar ciclo.
   */
  setChildren: (id: number, monitorIds: number[]) =>
    request<{ items: Monitor[] }>(`/monitors/${id}/children`, {
      method: "POST",
      body: JSON.stringify({ monitor_ids: monitorIds }),
    }),

  // ---------- dados ----------

  /**
   * summaries traz estado, disponibilidade, latência e a faixa já pronta
   * para todos os monitores.
   *
   * Uma requisição para a lista inteira, e nenhum cálculo aqui. Antes o
   * painel abria uma conexão por monitor e somava disponibilidade por
   * conta própria, com a tela de detalhe fazendo o mesmo numa cópia
   * separada — as duas discordavam sobre ausência de medição sem que nada
   * acusasse.
   */
  summaries: (params: { from?: string; to?: string; buckets?: number } = {}) =>
    request<{ items: SummaryDTO[]; from: string; to: string }>(`/summaries${query(params)}`),

  heartbeats: (id: number, params: { from?: string; to?: string; limit?: number }) =>
    request<{ items: Heartbeat[] }>(`/monitors/${id}/heartbeats${query(params)}`),

  rollups: (
    id: number,
    params: { from?: string; to?: string; resolution?: "hourly" | "daily" },
  ) => request<{ items: Rollup[]; resolution: string }>(`/monitors/${id}/rollups${query(params)}`),

  /**
   * events é a linha do tempo do monitor: mudanças de estado e falhas,
   * da mais recente para trás. A próxima página é a do next_cursor da
   * anterior, passado em cursor: o cursor carrega instante, tipo e chave,
   * e eventos do mesmo instante não se perdem entre páginas.
   */
  events: (
    id: number,
    params: { cursor?: string; limit?: number; type?: "state" | "failure" } = {},
  ) =>
    request<EventPage>(`/monitors/${id}/events${query(params)}`),

  /** problems é o que está errado agora, já ordenado pelo servidor. */
  problems: () => request<{ items: Problem[] }>("/problems"),

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

  incidents: (
    params: { monitor_id?: number; open?: string; limit?: number; cursor?: string } = {},
  ) => request<CursorPage<Incident>>(`/incidents${query(params)}`),

  /**
   * acknowledgeIncident é o "estou vendo": cala os lembretes daquela queda.
   * Pelo id do incidente, e não "a queda atual", para o reconhecimento não
   * passar para uma queda nova que a pessoa nem viu.
   */
  acknowledgeIncident: (monitorID: number, incidentID: number, note?: string) =>
    request<Incident>(`/monitors/${monitorID}/incidents/${incidentID}/ack`, {
      method: "POST",
      body: JSON.stringify(note ? { note } : {}),
    }),

  // ---------- contas ----------

  listUsers: () => request<{ items: User[] }>("/users"),

  createUser: (input: { username: string; password: string; role: Role; scope?: Scope }) =>
    request<User>("/users", { method: "POST", body: JSON.stringify(input) }),

  setUserRole: (id: number, role: Role) =>
    request<User>(`/users/${id}`, { method: "PUT", body: JSON.stringify({ role }) }),

  setUserScope: (id: number, role: Role, scope: Scope) =>
    request<User>(`/users/${id}`, { method: "PUT", body: JSON.stringify({ role, scope }) }),

  // ---------- grupos ----------

  listGroups: () => request<{ items: Group[] }>("/groups"),

  createMonitorGroup: (input: GroupInput) =>
    request<Group>("/groups", { method: "POST", body: JSON.stringify(input) }),

  updateMonitorGroup: (id: number, input: GroupInput) =>
    request<Group>(`/groups/${id}`, { method: "PUT", body: JSON.stringify(input) }),

  deleteMonitorGroup: (id: number) => request<void>(`/groups/${id}`, { method: "DELETE" }),

  /** Um cartão por grupo do escopo, com o pior estado e as contagens. */
  groupSummary: () => request<{ items: GroupSummary[] }>("/groups/summary"),

  groupChannels: (groupID: number) => request<{ items: Channel[] }>(`/groups/${groupID}/channels`),

  linkGroupChannel: (groupID: number, channelID: number) =>
    request<void>(`/groups/${groupID}/channels/${channelID}`, { method: "PUT" }),

  unlinkGroupChannel: (groupID: number, channelID: number) =>
    request<void>(`/groups/${groupID}/channels/${channelID}`, { method: "DELETE" }),

  deleteUser: (id: number) => request<void>(`/users/${id}`, { method: "DELETE" }),

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
