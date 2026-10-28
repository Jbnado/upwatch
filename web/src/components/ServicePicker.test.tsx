import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import type { Group, Monitor } from "../api/types";
import { ServicePicker } from "./ServicePicker";

/**
 * O seletor de serviços da página pública e do relato.
 *
 * O caso que o motivou: montar uma página com 2 serviços da Retaguarda ·
 * prod e 1 do RPA · prod, numa instalação onde "erp" e "banco" existem
 * em vários ambientes. Com a lista plana de nomes, não dava para saber
 * qual "erp" estava marcando.
 */

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return { id, name, slug: name, parent_id, path: pai ? `${pai} / ${name}` : name, created_at: "", updated_at: "" };
}

function monitor(id: number, name: string, group_id: number | null): Monitor {
  return {
    id,
    name,
    type: "http",
    target: "",
    interval_seconds: 60,
    timeout_seconds: 5,
    confirmation_threshold: 2,
    degraded_latency_ms: 0,
    group_id,
    enabled: true,
    tags: [],
    created_at: "",
    updated_at: "",
  };
}

const GRUPOS = [
  grupo(1, "RPA"),
  grupo(2, "prod", 1, "RPA"),
  grupo(3, "hml", 1, "RPA"),
  grupo(4, "Retaguarda"),
  grupo(5, "prod", 4, "Retaguarda"),
  grupo(6, "hml", 4, "Retaguarda"),
];

const MONITORES = [
  monitor(10, "api", 2),
  monitor(12, "api", 3),
  monitor(20, "fiscal", 5),
  monitor(21, "erp", 5),
  monitor(22, "banco", 5),
  monitor(23, "erp", 6),
];

function Montado({ inicial = [] as number[] }) {
  const [sel, setSel] = useState(new Set(inicial));
  return (
    <>
      <ServicePicker
        groups={GRUPOS}
        monitors={MONITORES}
        selected={sel}
        onChange={setSel}
        renderDetail={(id) => <span>detalhe {id}</span>}
      />
      <output>{JSON.stringify([...sel].sort())}</output>
    </>
  );
}

const caixa = (nome: RegExp) => screen.getByRole("checkbox", { name: nome });

describe("ServicePicker", () => {
  it("dá a cada caixa um nome que diz de qual grupo ela é", () => {
    // Para quem usa leitor de tela, "erp" duas vezes é a mesma ambiguidade
    // que motivou o seletor.
    render(<Montado inicial={[21, 23]} />);

    expect(caixa(/^erp.*Retaguarda · prod/)).toBeChecked();
    expect(caixa(/^erp.*Retaguarda · hml/)).toBeChecked();
  });

  it("marcar o ambiente marca todos os serviços dele", () => {
    render(<Montado />);

    fireEvent.click(caixa(/^prod.*Retaguarda/));

    expect(screen.getByRole("status")).toHaveTextContent("[20,21,22]");
  });

  it("mostra parcial no ambiente e no sistema com parte marcada", () => {
    render(<Montado inicial={[20, 21]} />);

    expect(caixa(/^prod.*Retaguarda/)).toBePartiallyChecked();
    expect(caixa(/^Retaguarda/)).toBePartiallyChecked();
    expect(caixa(/^RPA/)).not.toBeChecked();
  });

  it("abre os ambientes com algo marcado e fecha os outros", () => {
    render(<Montado inicial={[21]} />);

    expect(screen.getByRole("button", { name: /Recolher prod.*Retaguarda/ })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    // Fechado, o serviço não está na tela, mas o ambiente continua lá.
    expect(screen.queryByRole("checkbox", { name: /^api.*RPA · prod/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Abrir prod.*RPA/ }));
    fireEvent.click(caixa(/^api.*RPA · prod/));

    expect(screen.getByRole("status")).toHaveTextContent("[10,21]");
  });

  it("filtra a árvore pela busca, abrindo o que achou", () => {
    render(<Montado />);

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "erp" } });

    expect(screen.getAllByRole("checkbox", { name: /^erp/ })).toHaveLength(2);
    expect(screen.queryByRole("checkbox", { name: /^fiscal/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: /^RPA/ })).not.toBeInTheDocument();
  });

  it("na busca, marcar o grupo marca só o que está à vista", () => {
    // Buscar "erp", marcar "Retaguarda" e levar junto fiscal e banco seria
    // publicar o que a pessoa não estava vendo.
    render(<Montado />);

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "erp" } });
    fireEvent.click(caixa(/^Retaguarda/));

    expect(screen.getByRole("status")).toHaveTextContent("[21,23]");
  });

  it("diz quando a busca não acha nada", () => {
    render(<Montado />);

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "xyz" } });

    expect(screen.getByText(/Nenhum serviço com "xyz"/)).toBeInTheDocument();
  });

  it("mostra o detalhe só sob o serviço marcado", () => {
    // É onde mora o rótulo público: só faz sentido no que vai ser publicado.
    render(<Montado inicial={[21]} />);

    expect(screen.getByText("detalhe 21")).toBeInTheDocument();
    expect(screen.queryByText("detalhe 20")).not.toBeInTheDocument();
  });
});
