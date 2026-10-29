import { render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GroupSummary, Problem } from "../api/types";
import { Home } from "./Home";

/**
 * A tela inicial nova.
 *
 * O pedido foi explícito: só "Problemas agora" e os cartões, nada de
 * lista nem gráfico embaixo, e o problema precisa ser clicável e levar à
 * causa. Estes testes prendem esses três pontos e os dois desvios — a
 * instalação sem grupos e a conta com escopo.
 */

vi.mock("../api/client", () => ({
  api: {
    groupSummary: vi.fn(),
    problems: vi.fn(),
    listMonitors: vi.fn(),
    summaries: vi.fn(),
  },
}));

const { api } = await import("../api/client");

function cartao(over: Partial<GroupSummary>): GroupSummary {
  return {
    group_id: 1,
    parent_id: null,
    name: "RPA",
    path: "RPA",
    status: "up",
    total: 7,
    up: 7,
    degraded: 0,
    down: 0,
    unknown: 0,
    paused: 0,
    affected: 0,
    problems: 0,
    ...over,
  };
}

const RPA = cartao({ group_id: 1, name: "RPA", path: "RPA", total: 14, up: 13, down: 1, problems: 1, status: "down" });
const RPA_HML = cartao({ group_id: 2, parent_id: 1, name: "hml", path: "RPA / hml" });
const RPA_PROD = cartao({
  group_id: 3,
  parent_id: 1,
  name: "prod",
  path: "RPA / prod",
  status: "down",
  up: 6,
  down: 1,
  problems: 1,
});

const BFF: Problem = {
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

beforeEach(() => {
  vi.mocked(api.listMonitors).mockResolvedValue({ items: [], has_more: false });
  vi.mocked(api.summaries).mockResolvedValue({ items: [], from: "", to: "" });
});

describe("Home", () => {
  it("mostra o problema como link para a falha, com grupo, tempo e causa", async () => {
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [RPA, RPA_HML, RPA_PROD] });
    vi.mocked(api.problems).mockResolvedValue({ items: [BFF] });

    render(<Home podeEscrever={false} />);

    const secao = await screen.findByRole("region", { name: /problemas agora/ });
    const link = within(secao).getByRole("link");
    expect(link).toHaveAttribute("href", `/monitors/12#falha-${Date.parse(BFF.last_check_at!)}`);
    expect(link).toHaveTextContent("RPA · prod");
    expect(link).toHaveTextContent("fora do ar");
    expect(link).toHaveTextContent("há 12 min");
    // Resumo legível na frente, o texto técnico inteiro no title.
    expect(link).toHaveTextContent("conexão recusada");
    expect(within(link).getByTitle("connection refused")).toBeInTheDocument();
  });

  it("diz quantos dependentes a queda deixou sem sinal", async () => {
    // Os filhos calados não aparecem na lista; sem a conta, quem vê só a
    // sonda caída não sabe que as verificações dela também pararam.
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [RPA, RPA_HML, RPA_PROD] });
    vi.mocked(api.problems).mockResolvedValue({ items: [{ ...BFF, dependents: 4 }] });

    render(<Home podeEscrever={false} />);

    const secao = await screen.findByRole("region", { name: /problemas agora/ });
    expect(within(secao).getByRole("link")).toHaveTextContent("4 dependentes sem sinal");
  });

  it("dá um cartão por ambiente, cada um um link inteiro com a palavra do estado", async () => {
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [RPA, RPA_HML, RPA_PROD] });
    vi.mocked(api.problems).mockResolvedValue({ items: [BFF] });

    render(<Home podeEscrever={false} />);

    const sistema = await screen.findByRole("region", { name: "RPA" });
    const cartoes = within(sistema).getAllByRole("link");
    expect(cartoes.map((c) => c.getAttribute("href"))).toEqual(["/groups/2", "/groups/3"]);
    expect(cartoes[1]).toHaveTextContent("prod");
    expect(cartoes[1]).toHaveTextContent("1 problema");
    expect(cartoes[0]).toHaveTextContent("ok");
  });

  it("não mostra a seção de problemas quando está tudo bem, nem lista embaixo", async () => {
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [RPA_HML] });
    vi.mocked(api.problems).mockResolvedValue({ items: [] });

    render(<Home podeEscrever={false} />);

    await screen.findByRole("region", { name: "RPA" });
    expect(screen.queryByText("problemas agora")).not.toBeInTheDocument();
    // A lista de monitores é da tela do grupo; aqui ela não é nem pedida.
    expect(api.listMonitors).not.toHaveBeenCalled();
  });

  it("cai direto na lista numa instalação sem grupos", async () => {
    vi.mocked(api.groupSummary).mockResolvedValue({
      items: [cartao({ group_id: null, name: "Sem grupo", path: "Sem grupo" })],
    });
    vi.mocked(api.problems).mockResolvedValue({ items: [] });

    render(<Home podeEscrever />);

    expect(await screen.findByText("Nenhum alvo sendo vigiado")).toBeInTheDocument();
    expect(api.listMonitors).toHaveBeenCalled();
  });

  it("mostra à conta com escopo só os cartões dela, sob o sistema certo", async () => {
    // O servidor recorta; a tela não pode perder o nome do sistema quando
    // o escopo alcança o ambiente e não o sistema.
    const retProd = cartao({ group_id: 5, parent_id: 4, name: "prod", path: "Retaguarda / prod" });
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [retProd] });
    vi.mocked(api.problems).mockResolvedValue({ items: [] });

    render(<Home podeEscrever={false} />);

    const sistema = await screen.findByRole("region", { name: "Retaguarda" });
    expect(within(sistema).getAllByRole("link")).toHaveLength(1);
    expect(screen.queryByText("RPA")).not.toBeInTheDocument();
    expect(screen.queryByText("Sem grupo")).not.toBeInTheDocument();
  });

  it("explica a conta sem grupo nenhum em vez de mostrar painel vazio", async () => {
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [] });
    vi.mocked(api.problems).mockResolvedValue({ items: [] });

    render(<Home podeEscrever={false} />);

    expect(await screen.findByText("Nenhum grupo no seu acesso")).toBeInTheDocument();
  });
});

describe("Home: grupo sem monitor", () => {
  // O servidor passou a mandar "empty" para o grupo sem monitor nenhum, em
  // vez de "unknown". "unknown" é monitor que deveria responder e não está
  // sendo medido; o grupo vazio não tem o que medir. Pintá-lo com a cor de
  // "sem medição" alarmaria por um grupo recém-criado.
  it("mostra o cartão como vazio, sem cor de problema", async () => {
    const vazio = cartao({
      group_id: 2,
      parent_id: 1,
      name: "hml",
      path: "RPA / hml",
      status: "empty",
      total: 0,
      up: 0,
    });
    vi.mocked(api.groupSummary).mockResolvedValue({ items: [RPA_PROD, vazio] });
    vi.mocked(api.problems).mockResolvedValue({ items: [BFF] });

    render(<Home podeEscrever={false} />);

    const sistema = await screen.findByRole("region", { name: "RPA" });
    const card = within(sistema).getAllByRole("link")[1]!;
    expect(card).toHaveAttribute("href", "/groups/2");
    expect(card).toHaveTextContent("vazio");
    expect(card.className).not.toMatch(/down|degraded|sunken/);
    // O ponto também diz "vazio" a quem lê a tela, e não "sem medição".
    expect(card).not.toHaveTextContent("sem medição");
  });
});
