import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import type { Group, Scope } from "../api/types";
import { ScopePicker } from "./Users";

/**
 * A escolha de acesso por conta, agora com vários grupos.
 *
 * A caixa precisa dizer a verdade sobre o que o servidor vai aplicar:
 * ambiente coberto pelo sistema aparece marcado e travado, e quem tem
 * escopo não vê a opção de conceder tudo, que o servidor recusaria.
 */

function grupo(id: number, name: string, parent_id: number | null = null, pai = ""): Group {
  return {
    id,
    name,
    slug: name,
    parent_id,
    path: pai ? `${pai} / ${name}` : name,
    created_at: "",
    updated_at: "",
  };
}

const GRUPOS = [
  grupo(1, "RPA"),
  grupo(2, "hml", 1, "RPA"),
  grupo(3, "prod", 1, "RPA"),
  grupo(4, "Retaguarda"),
  grupo(5, "prod", 4, "Retaguarda"),
];

function Montado({ inicial, podeTodos }: { inicial: Scope; podeTodos: boolean }) {
  const [escopo, setEscopo] = useState(inicial);
  return (
    <>
      <ScopePicker
        value={escopo}
        onChange={setEscopo}
        groups={GRUPOS}
        podeTodos={podeTodos}
        name="teste"
      />
      <output>{JSON.stringify(escopo)}</output>
    </>
  );
}

describe("ScopePicker", () => {
  it("marca vários grupos", () => {
    render(<Montado inicial={{ all_groups: false, group_ids: [] }} podeTodos />);

    fireEvent.click(screen.getByRole("checkbox", { name: /Retaguarda/ }));
    fireEvent.click(screen.getAllByRole("checkbox", { name: /prod/ })[0]!);

    expect(screen.getByRole("status")).toHaveTextContent('{"all_groups":false,"group_ids":[4,3]}');
  });

  it("trava os ambientes cobertos pelo sistema marcado", () => {
    render(<Montado inicial={{ all_groups: false, group_ids: [1] }} podeTodos />);

    const hml = screen.getByRole("checkbox", { name: /hml/ });
    expect(hml).toBeChecked();
    expect(hml).toBeDisabled();
    expect(screen.getAllByText("pelo sistema")).toHaveLength(2);
  });

  it("não oferece todos os grupos a quem tem escopo", () => {
    render(<Montado inicial={{ all_groups: false, group_ids: [4] }} podeTodos={false} />);

    expect(screen.queryByRole("radio", { name: "Todos os grupos" })).not.toBeInTheDocument();
  });

  it("avisa quando nenhum grupo foi escolhido", () => {
    render(<Montado inicial={{ all_groups: true, group_ids: [] }} podeTodos />);

    fireEvent.click(screen.getByRole("radio", { name: "Só os grupos escolhidos" }));

    expect(screen.getByText("Escolha pelo menos um grupo.")).toBeInTheDocument();
  });
});
