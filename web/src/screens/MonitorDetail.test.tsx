import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, type Channel, type Monitor, type MonitorEvent, type Problem, type Summary, type User } from "../api/types";
import { MonitorDetail } from "./MonitorDetail";

/**
 * O detalhe do monitor.
 *
 * O que se prende aqui: o quadro de estado existe só com problema, a aba
 * de eventos não contradiz o quadro, a falha da âncora aparece em
 * destaque e as abas têm a semântica de abas.
 */

vi.mock("../api/client", () => ({
  api: {
    getMonitor: vi.fn(),
    summaries: vi.fn(),
    listGroups: vi.fn(),
    problems: vi.fn(),
    heartbeats: vi.fn(),
    events: vi.fn(),
    incidents: vi.fn(),
    monitorChannels: vi.fn(),
    groupChannels: vi.fn(),
    listChannels: vi.fn(),
    linkChannel: vi.fn(),
    unlinkChannel: vi.fn(),
    listMonitors: vi.fn(),
    setChildren: vi.fn(),
    exportUrl: () => "/export",
  },
}));

const { api } = await import("../api/client");

const EU: User = {
  id: 1,
  username: "ana",
  role: "viewer",
  scope: { all_groups: true, group_ids: [] },
  created_at: "",
  updated_at: "",
};

const BFF: Monitor = {
  id: 12,
  name: "bff",
  type: "http",
  target: "http://bff:3000/health",
  interval_seconds: 20,
  timeout_seconds: 5,
  confirmation_threshold: 3,
  degraded_latency_ms: 0,
  group_id: 3,
  enabled: true,
  tags: [],
  created_at: "",
  updated_at: "",
};

function resumo(status: Summary["status"]): Summary {
  return {
    monitor_id: 12,
    status,
    source: "raw",
    uptime_percent: 99.2,
    latency_p50_ms: 120,
    latency_p95_ms: 180,
    latency_p99_ms: 300,
    last_check_at: null,
    last_check_status: status,
    up: 0,
    degraded: 0,
    down: 0,
    unknown: 0,
    series: [],
  };
}

const QUEDA: Problem = {
  monitor_id: 12,
  name: "bff",
  type: "http",
  group_id: 3,
  group_path: "RPA / prod",
  status: "down",
  since: new Date(Date.now() - 12 * 60_000).toISOString(),
  cause: "connection refused",
  last_check_at: "2026-10-28T11:43:00Z",
  dependents: 0,
};

const FALHA: MonitorEvent = {
  type: "failure",
  at: "2026-10-28T11:43:00Z",
  status: "down",
  message: "connection refused",
};

function cenario({
  status,
  problema,
  eventos,
}: {
  status: Summary["status"];
  problema: Problem | null;
  eventos: MonitorEvent[];
}) {
  vi.mocked(api.getMonitor).mockResolvedValue(BFF);
  vi.mocked(api.summaries).mockResolvedValue({ items: [resumo(status)], from: "", to: "" });
  vi.mocked(api.listGroups).mockResolvedValue({
    items: [
      { id: 1, name: "RPA", slug: "rpa", parent_id: null, path: "RPA", created_at: "", updated_at: "" },
      { id: 3, name: "prod", slug: "prod", parent_id: 1, path: "RPA / prod", created_at: "", updated_at: "" },
    ],
  });
  vi.mocked(api.problems).mockResolvedValue({ items: problema ? [problema] : [] });
  vi.mocked(api.heartbeats).mockResolvedValue({
    items: [{ status: "up" }, { status: "down" }, { status: "down" }, { status: "down" }].map(
      (b, i) => ({ ...b, timestamp: String(i), latency_ms: 0, probe_id: "local" }),
    ) as never,
  });
  vi.mocked(api.events).mockResolvedValue({ items: eventos, has_more: false });
}

beforeEach(() => {
  vi.mocked(api.listMonitors).mockResolvedValue({ items: [BFF], has_more: false });
  vi.mocked(api.incidents).mockResolvedValue({ items: [], has_more: false });
  vi.mocked(api.monitorChannels).mockResolvedValue({ items: [] });
  vi.mocked(api.groupChannels).mockResolvedValue({ items: [] });
});

describe("MonitorDetail", () => {
  it("abre o quadro de estado com tempo, causa e falhas seguidas quando está fora do ar", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [FALHA] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const quadro = await screen.findByRole("region", { name: "estado atual" });
    expect(quadro).toHaveTextContent("fora do ar há 12 min");
    expect(quadro).toHaveTextContent("conexão recusada");
    expect(within(quadro).getByTitle("connection refused")).toBeInTheDocument();
    expect(await within(quadro).findByText(/3 falhas seguidas · desde/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /RPA · prod/ })).toBeInTheDocument();
  });

  it("não tem quadro de estado com o monitor no ar", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    expect(await screen.findByText("Nenhuma falha nem mudança de estado registrada.")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "estado atual" })).not.toBeInTheDocument();
  });

  it("não diz que nada mudou quando está fora do ar e a linha do tempo está vazia", async () => {
    // O defeito da #49: "Nenhuma mudança no período" sob um monitor caído.
    cenario({ status: "down", problema: QUEDA, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    expect(await screen.findByText(/O monitor está fora do ar/)).toBeInTheDocument();
    expect(screen.queryByText(/Nenhuma mudança no período/)).not.toBeInTheDocument();
  });

  it("destaca a falha que veio na âncora do endereço", async () => {
    const ancora = `falha-${Date.parse(FALHA.at)}`;
    cenario({
      status: "down",
      problema: QUEDA,
      eventos: [FALHA, { ...FALHA, at: "2026-10-28T11:42:40Z" }],
    });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    const linhas = await screen.findAllByRole("listitem");
    const destaque = linhas.filter((l) => l.getAttribute("aria-current") === "true");
    expect(destaque).toHaveLength(1);
    expect(destaque[0]).toHaveAttribute("id", ancora);
    expect(destaque[0]).toHaveTextContent("do link");
  });

  it("tem abas de verdade, com a de eventos selecionada e as setas andando", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const lista = await screen.findByRole("tablist");
    const abas = within(lista).getAllByRole("tab");
    expect(abas.map((a) => a.textContent)).toEqual(["Eventos", "Quedas", "Avisos"]);
    expect(abas[0]).toHaveAttribute("aria-selected", "true");
    expect(abas[1]).toHaveAttribute("tabindex", "-1");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", abas[0]!.id);

    fireEvent.keyDown(abas[0]!, { key: "ArrowRight" });
    expect(window.location.pathname).toBe("/monitors/12/quedas");
  });

  it("esconde editar e remover de quem só observa", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("tablist");
    expect(screen.queryByRole("button", { name: "Editar" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Mais ações" }));
    expect(screen.getAllByRole("menuitem").map((i) => i.textContent)).toEqual([
      "Exportar CSV (24 h)",
      "Exportar JSON (24 h)",
    ]);
  });
});

describe("MonitorDetail: falhas repetidas", () => {
  const recusada = (i: number): MonitorEvent => ({
    type: "failure",
    at: new Date(Date.UTC(2026, 9, 28, 11, 43, 0) - i * 20_000).toISOString(),
    status: "down",
    message: `dial tcp 127.0.0.1:1: connectex: Nenhuma conexão pôde ser feita porque a máquina de destino as recusou ativamente. (${i})`,
  });
  const nove = Array.from({ length: 9 }, (_, i) => recusada(i));

  it("junta falhas iguais numa linha que abre para as individuais", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const linha = await screen.findByRole("button", { name: /conexão recusada.*9 vezes/ });
    expect(linha).toHaveAttribute("aria-expanded", "false");
    // Fechado, nenhuma das nove aparece com o texto técnico.
    expect(screen.queryByText(/connectex/)).not.toBeInTheDocument();

    fireEvent.click(linha);

    expect(linha).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByText(/connectex/)).toHaveLength(9);
  });

  it("abre o grupo que contém a falha do link, com ela marcada", async () => {
    const alvo = nove[4]!;
    const ancora = `falha-${Date.parse(alvo.at)}`;
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    const linha = await screen.findByRole("button", { name: /9 vezes/ });
    expect(linha).toHaveAttribute("aria-expanded", "true");
    const marcada = document.getElementById(ancora);
    expect(marcada).toHaveAttribute("aria-current", "true");
    expect(marcada).toHaveTextContent("do link");
  });

  it("aberto pelo link, mostra só a vizinhança da falha, com o resto a um clique", async () => {
    // Um grupo de trinta falhas iguais aberto inteiro devolvia a tela
    // comprida que o agrupamento veio encurtar. Quem chega pelo link quer
    // a falha e o entorno; o resto continua ali, a um clique.
    const ancora = `falha-${Date.parse(nove[4]!.at)}`;
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    await screen.findByRole("button", { name: /9 vezes/ });
    const visiveis = () => screen.getAllByText(/connectex/).map((e) => e.textContent);
    expect(visiveis()).toEqual([2, 3, 4, 5, 6].map((i) => nove[i]!.message));

    const antes = screen.getByRole("button", { name: "mostrar as outras 2 mais recentes" });
    const depois = screen.getByRole("button", { name: "mostrar as outras 2 mais antigas" });

    fireEvent.click(antes);
    expect(visiveis()).toHaveLength(7);
    fireEvent.click(depois);
    expect(visiveis()).toHaveLength(9);
    expect(screen.queryByRole("button", { name: /mostrar as outras/ })).not.toBeInTheDocument();
  });

  it("aberto por clique, mostra o grupo inteiro mesmo com a falha do link dentro", async () => {
    const ancora = `falha-${Date.parse(nove[4]!.at)}`;
    cenario({ status: "down", problema: QUEDA, eventos: nove });

    render(<MonitorDetail id={12} tab="eventos" anchor={ancora} eu={EU} />);

    const linha = await screen.findByRole("button", { name: /9 vezes/ });
    fireEvent.click(linha);
    fireEvent.click(linha);

    expect(screen.getAllByText(/connectex/)).toHaveLength(9);
  });

  it("mostra o texto técnico de uma falha solta ao abrir a linha", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [FALHA] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const linha = await screen.findByRole("button", { name: /conexão recusada/ });
    const painel = screen.getByRole("tabpanel");
    expect(within(painel).queryByText("connection refused")).not.toBeInTheDocument();

    fireEvent.click(linha);

    expect(within(painel).getByText("connection refused")).toBeInTheDocument();
  });

  it("une o grupo que atravessa a página ao carregar os mais antigos", async () => {
    cenario({ status: "down", problema: QUEDA, eventos: [] });
    vi.mocked(api.events)
      .mockResolvedValueOnce({ items: nove.slice(0, 5), has_more: true, next_cursor: "c1" })
      .mockResolvedValueOnce({ items: nove.slice(5), has_more: false });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("button", { name: /5 vezes/ });
    fireEvent.click(screen.getByRole("button", { name: "Carregar mais antigos" }));

    expect(await screen.findByRole("button", { name: /9 vezes/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /4 vezes/ })).not.toBeInTheDocument();
  });
});

describe("MonitorDetail: paginação dos eventos", () => {
  // O servidor trocou o instante (next_before) por um cursor composto
  // (next_cursor, devolvido em ?cursor=), para eventos do mesmo instante
  // não se perderem entre páginas. A tela que ainda lesse next_before
  // pararia na primeira página sem dizer nada: o botão de mais antigos
  // simplesmente não apareceria.
  const evento = (i: number): MonitorEvent => ({
    type: "state",
    at: new Date(Date.UTC(2026, 9, 28, 11, 0, 0) - i * 3_600_000).toISOString(),
    status: i % 2 === 0 ? "up" : "down",
    from: i % 2 === 0 ? "down" : "up",
  });

  it("segue o next_cursor de página em página até o fim", async () => {
    vi.mocked(api.events).mockClear();
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.events)
      .mockResolvedValueOnce({ items: [evento(0), evento(1)], has_more: true, next_cursor: "c1" })
      .mockResolvedValueOnce({ items: [evento(2), evento(3)], has_more: true, next_cursor: "c2" })
      .mockResolvedValueOnce({ items: [evento(4)], has_more: false });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    fireEvent.click(await screen.findByRole("button", { name: "Carregar mais antigos" }));
    await vi.waitFor(() => expect(api.events).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.events).mock.calls[1]![1]).toMatchObject({ cursor: "c1" });

    fireEvent.click(await screen.findByRole("button", { name: "Carregar mais antigos" }));
    await vi.waitFor(() => expect(api.events).toHaveBeenCalledTimes(3));
    expect(vi.mocked(api.events).mock.calls[2]![1]).toMatchObject({ cursor: "c2" });

    // Sem has_more, acabou: o botão some em vez de pedir uma página vazia.
    await vi.waitFor(() =>
      expect(screen.queryByRole("button", { name: "Carregar mais antigos" })).not.toBeInTheDocument(),
    );
    // A primeira página não leva cursor: começa do mais recente.
    expect(vi.mocked(api.events).mock.calls[0]![1]).not.toHaveProperty("cursor");
  });
});

describe("MonitorDetail: estado confirmado e última verificação", () => {
  // O resumo passou a trazer o estado confirmado em status, o mesmo do
  // cartão e de "Problemas agora", e a última batida em
  // last_check_status. A falha ainda não confirmada só aparece pela
  // última batida; sem olhar para ela, o quadro sumiria justamente no
  // começo de uma queda.
  it("diz que está falhando, sem afirmar queda, quando só a última verificação falhou", async () => {
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.summaries).mockResolvedValue({
      items: [{ ...resumo("up"), last_check_status: "down" }],
      from: "",
      to: "",
    });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const quadro = await screen.findByRole("region", { name: "estado atual" });
    expect(quadro).toHaveTextContent("falhando");
    expect(quadro).toHaveTextContent("ainda não confirmado como queda");
    expect(quadro).not.toHaveTextContent("fora do ar");
  });

  it("não abre quadro quando o confirmado e a última verificação estão no ar", async () => {
    cenario({ status: "up", problema: null, eventos: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("tablist");
    expect(screen.queryByRole("region", { name: "estado atual" })).not.toBeInTheDocument();
  });

  it("não chama de falha a última verificação sem medição", async () => {
    // "unknown" é a sonda dizendo que a rede do próprio servidor caiu: o
    // alvo não falhou, só não foi medido.
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.summaries).mockResolvedValue({
      items: [{ ...resumo("up"), last_check_status: "unknown" }],
      from: "",
      to: "",
    });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    await screen.findByRole("tablist");
    expect(screen.queryByRole("region", { name: "estado atual" })).not.toBeInTheDocument();
  });
});

const ADMIN_TOTAL: User = { ...EU, id: 2, username: "bia", role: "admin" };
const ADMIN_RESTRITO: User = {
  ...EU,
  id: 3,
  username: "caio",
  role: "admin",
  scope: { all_groups: false, group_ids: [3] },
};

describe("MonitorDetail: avisos e escopo", () => {
  const DISCORD: Channel = {
    id: 7,
    name: "plantão",
    type: "discord",
    enabled: true,
    created_at: "",
    updated_at: "",
  };

  it("não oferece vincular canal à conta restrita, e diz por quê", async () => {
    // O servidor responde 403: o canal é de todos os grupos, e pendurá-lo
    // no próprio monitor mexe no aviso dos outros. Caixas que sempre
    // voltam atrás seriam piores que caixa nenhuma.
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.monitorChannels).mockResolvedValue({ items: [DISCORD] });

    render(<MonitorDetail id={12} tab="avisos" eu={ADMIN_RESTRITO} />);

    expect(await screen.findByText("plantão")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByText(/só quem tem acesso a todos os grupos/i)).toBeInTheDocument();
    expect(api.listChannels).not.toHaveBeenCalled();
  });

  it("mostra a recusa do servidor ao vincular, em vez de desfazer a caixa calado", async () => {
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.listChannels).mockResolvedValue({ items: [DISCORD] });
    vi.mocked(api.monitorChannels).mockResolvedValue({ items: [] });
    vi.mocked(api.linkChannel).mockRejectedValue(
      new ApiError(403, "forbidden", "seu escopo não permite isso"),
    );

    render(<MonitorDetail id={12} tab="avisos" eu={ADMIN_TOTAL} />);

    fireEvent.click(await screen.findByRole("checkbox", { name: /plantão/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("seu escopo não permite isso");
    await vi.waitFor(() =>
      expect(screen.getByRole("checkbox", { name: /plantão/ })).not.toBeChecked(),
    );
  });
});

describe("MonitorDetail: push", () => {
  const PUSH: Monitor = {
    ...BFF,
    type: "push",
    target: "",
    config: { token: "AbCdEfGhIjKlMnOpQrSt", schedule: "2 9 * * *", timezone: "America/Sao_Paulo" },
  };

  it("não monta endereço com a máscara que o observador recebe no lugar do token", async () => {
    // Para quem só lê, o servidor devolve o token como "***". Um endereço
    // montado com ele não funciona, e o botão copiaria um endereço falso.
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.getMonitor).mockResolvedValue({ ...PUSH, config: { ...PUSH.config, token: "***" } });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const sinal = await screen.findByRole("region", { name: "sinal do push" });
    expect(sinal).toHaveTextContent("sinal em dia");
    expect(sinal).not.toHaveTextContent("/api/v1/push/");
    expect(within(sinal).queryByRole("button", { name: "Copiar endereço" })).not.toBeInTheDocument();
  });

  it("dá ao administrador o endereço com o token cortado e a cópia", async () => {
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.getMonitor).mockResolvedValue(PUSH);

    render(<MonitorDetail id={12} tab="eventos" eu={ADMIN_TOTAL} />);

    const sinal = await screen.findByRole("region", { name: "sinal do push" });
    expect(sinal).toHaveTextContent("/api/v1/push/AbCd…");
    expect(within(sinal).getByRole("button", { name: "Copiar endereço" })).toBeInTheDocument();
  });
});

describe("MonitorDetail: dependências", () => {
  const SONDA: Monitor = { ...BFF, id: 9, name: "sonda vm-01", type: "push", target: "", group_id: 1 };
  const REDIS: Monitor = { ...BFF, id: 20, name: "redis", parent_id: 12 };
  const SOLTO: Monitor = { ...BFF, id: 21, name: "postgres" };
  const FILA: Monitor = { ...BFF, id: 22, name: "fila" };

  it("mostra o calado pelo pai como sem sinal, e não como queda própria", async () => {
    // Caso real: a VM da sonda perdeu a saída, e cada verificação dela
    // aparecia fora do ar como se o serviço tivesse caído.
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.getMonitor).mockResolvedValue({ ...BFF, parent_id: 9 });
    vi.mocked(api.summaries).mockResolvedValue({
      items: [{ ...resumo("up"), affected_by: { id: 9, name: "sonda vm-01" } }],
      from: "",
      to: "",
    });
    vi.mocked(api.listMonitors).mockResolvedValue({ items: [SONDA, { ...BFF, parent_id: 9 }], has_more: false });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const quadro = await screen.findByRole("region", { name: "estado atual" });
    expect(quadro).toHaveTextContent("sem sinal — sonda vm-01 fora");
    expect(quadro).not.toHaveTextContent("fora do ar");
    expect(within(quadro).getByRole("link", { name: "sonda vm-01" })).toHaveAttribute("href", "/monitors/9");
  });

  it("lista quem depende deste monitor", async () => {
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.listMonitors).mockResolvedValue({ items: [BFF, REDIS, SOLTO], has_more: false });

    render(<MonitorDetail id={12} tab="eventos" eu={EU} />);

    const secao = await screen.findByRole("region", { name: "dependentes" });
    expect(within(secao).getByRole("link", { name: "redis" })).toHaveAttribute("href", "/monitors/20");
    expect(secao).not.toHaveTextContent("postgres");
  });

  it("liga vários monitores a este de uma vez", async () => {
    // O caso comum é ligar as quatro verificações de uma sonda ao sinal
    // de vida dela; editar uma a uma é onde se esquece alguma.
    cenario({ status: "up", problema: null, eventos: [] });
    vi.mocked(api.listMonitors).mockResolvedValue({ items: [BFF, REDIS, SOLTO, FILA], has_more: false });
    vi.mocked(api.setChildren).mockResolvedValue({ items: [] });

    render(<MonitorDetail id={12} tab="eventos" eu={ADMIN_TOTAL} />);
    await screen.findByRole("tablist");

    fireEvent.click(screen.getByRole("button", { name: "Mais ações" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Ligar dependentes" }));
    const secao = await screen.findByRole("region", { name: "dependentes" });
    fireEvent.click(within(secao).getByRole("checkbox", { name: "postgres" }));
    fireEvent.click(within(secao).getByRole("checkbox", { name: "fila" }));
    // Quem já depende deste não é oferecido de novo.
    expect(within(secao).queryByRole("checkbox", { name: "redis" })).not.toBeInTheDocument();
    fireEvent.click(within(secao).getByRole("button", { name: "Ligar 2 monitores" }));

    await vi.waitFor(() => expect(api.setChildren).toHaveBeenCalledWith(12, expect.arrayContaining([21, 22])));
    expect(vi.mocked(api.setChildren).mock.lastCall?.[1]).toHaveLength(2);
  });
});
