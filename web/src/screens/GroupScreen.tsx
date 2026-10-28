import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Alert, Loading } from "../components/ui";
import { Board } from "./Board";

/**
 * GroupScreen resolve o grupo do endereço e mostra a lista dele.
 *
 * O grupo vem do servidor, recortado pelo escopo: um grupo de fora
 * responde 404 igual a um que não existe, e a tela diz só isso.
 */
export function GroupScreen({ id, podeEscrever }: { id: number | null; podeEscrever: boolean }) {
  const [path, setPath] = useState<string | null>(id === null ? "" : null);
  const [erro, setErro] = useState<string | null>(null);

  useEffect(() => {
    if (id === null) {
      setPath("");
      return;
    }
    setPath(null);
    setErro(null);
    api
      .listGroups()
      .then(({ items }) => {
        const g = items.find((x) => x.id === id);
        if (g) setPath(g.path);
        else setErro("Grupo não encontrado.");
      })
      .catch((e: unknown) =>
        setErro(e instanceof Error ? e.message : "Não foi possível carregar o grupo."),
      );
  }, [id]);

  if (erro) {
    return (
      <div className="mx-auto max-w-6xl px-5 py-6">
        <Alert>{erro}</Alert>
      </div>
    );
  }
  if (path === null) {
    return (
      <div className="mx-auto max-w-6xl px-5 py-6">
        <Loading what="o grupo" />
      </div>
    );
  }

  // key no grupo: trocar de grupo zera busca e filtro, que eram do outro.
  return <Board key={id ?? "sem-grupo"} podeEscrever={podeEscrever} group={{ id, path }} />;
}
