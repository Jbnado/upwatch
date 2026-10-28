import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { InlineLink, TextLink } from "./ui";

// O cursor de mão e a área ampliada vivem no CSS, que o ambiente de
// teste não aplica; o que dá para garantir aqui é que as ações em texto
// usam a classe que os carrega. O efeito em si foi conferido no
// navegador, com getComputedStyle.
describe("ação em texto", () => {
  // Ação em texto tem o tamanho da palavra; sem a área ampliada, acertar
  // "voltar" com o mouse exige mira e no celular vira loteria.
  it("TextLink e InlineLink usam a área de clique ampliada", () => {
    render(
      <>
        <TextLink>voltar</TextLink>
        <InlineLink to={{ name: "board" }}>painel</InlineLink>
      </>,
    );
    expect(screen.getByRole("button", { name: "voltar" }).className).toContain("text-action");
    expect(screen.getByRole("link", { name: "painel" }).className).toContain("text-action");
  });
});
