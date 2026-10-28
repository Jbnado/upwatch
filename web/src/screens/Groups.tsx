import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Group } from "../api/types";
import { Alert, Button, Field, Input, Loading, Nothing, Select } from "../components/ui";

/**
 * Grupos de monitores: Sistema → Ambiente.
 *
 * Gestão mínima — criar e apagar —, o bastante para cadastrar monitores
 * em grupos e conceder escopo. A tela de grupo de verdade vem com a nova
 * tela inicial.
 */
export function Groups() {
  const [grupos, setGrupos] = useState<Group[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [nome, setNome] = useState("");
  const [pai, setPai] = useState("");

  const carregar = useCallback(async () => {
    const { items } = await api.listGroups();
    setGrupos(items);
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setErro(null);
    try {
      await api.createMonitorGroup({ name: nome, parent_id: pai ? Number(pai) : undefined });
      setNome("");
      setPai("");
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível criar o grupo.");
    }
  }

  async function apagar(g: Group) {
    if (!confirm(`Apagar o grupo "${g.path}"?`)) return;
    setErro(null);
    try {
      await api.deleteMonitorGroup(g.id);
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível apagar o grupo.");
    }
  }

  // Só sistema (grupo de topo) pode ser pai: dois níveis no máximo.
  const sistemas = (grupos ?? []).filter((g) => g.parent_id === null);

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Grupos</h2>
        <p className="text-body text-ink-2">
          Sistema e ambiente — RPA → prod. Cada monitor fica num ambiente (ou num sistema sem
          ambientes), e o acesso das contas é dado por grupo.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <form
        onSubmit={criar}
        className="grid gap-4 rounded-sm border border-line-strong p-4 sm:grid-cols-[minmax(0,1fr)_200px_auto] sm:items-end"
      >
        <Field label="nome">
          <Input value={nome} onChange={(e) => setNome(e.target.value)} placeholder="prod" required />
        </Field>
        <Field label="dentro de">
          <Select value={pai} onChange={(e) => setPai(e.target.value)}>
            <option value="">— nenhum (sistema)</option>
            {sistemas.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </Select>
        </Field>
        <Button type="submit" variant="primary">
          Criar grupo
        </Button>
      </form>

      {grupos === null ? (
        <Loading what="grupos" />
      ) : grupos.length === 0 ? (
        <Nothing>Nenhum grupo. Os monitores ficam em "Sem grupo".</Nothing>
      ) : (
        <ul className="border-t border-line">
          {grupos.map((g) => (
            <li
              key={g.id}
              className="flex items-center justify-between gap-4 border-b border-line py-2.5"
            >
              <span className="truncate text-body">{g.path}</span>
              <Button size="sm" variant="danger" onClick={() => apagar(g)}>
                Apagar
              </Button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
