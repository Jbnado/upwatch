import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Channel, type Group, type Scope } from "../api/types";
import { Alert, Button, Checkbox, Field, Input, Loading, Nothing, Select } from "../components/ui";
import { crumb } from "../lib/groups";
import { NO_CHANNEL_LINK_HINT, canLinkChannels, canRenameGroup } from "../lib/scope";
import { channelTypeLabel } from "./Channels";

/**
 * Grupos de monitores: Sistema → Ambiente, dois níveis no máximo.
 *
 * A lista é a própria árvore — ambiente recuado sob o sistema — porque é
 * assim que se pensa nela ("o prod do RPA"), e porque uma lista plana de
 * caminhos esconde qual sistema está vazio.
 *
 * Apagar grupo em uso é recusado antes de perguntar, dizendo o porquê e o
 * que fazer: um "tem certeza?" seguido de um erro seria a pior ordem.
 *
 * Pela mesma razão, a conta restrita não vê oferecido o que o servidor
 * recusaria: renomear grupo cujos irmãos ela não enxerga e vincular
 * canais. Os botões ficam, desligados, apontando para a explicação.
 */
export function Groups({ scope }: { scope: Scope }) {
  const podeCriarSistema = scope.all_groups;
  const [grupos, setGrupos] = useState<Group[] | null>(null);
  const [monitores, setMonitores] = useState<Map<number, number>>(new Map());
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const [{ items }, resumo] = await Promise.all([api.listGroups(), api.groupSummary()]);
    setGrupos(items);
    setMonitores(
      new Map(
        resumo.items
          .filter((c) => c.group_id !== null)
          .map((c) => [c.group_id as number, c.total]),
      ),
    );
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const todos = grupos ?? [];
  const sistemas = todos.filter((g) => g.parent_id === null);
  // Quem tem escopo pode alcançar o ambiente sem alcançar o sistema; o
  // ambiente órfão na lista aparece sozinho, no fim.
  const soltos = todos.filter(
    (g) => g.parent_id !== null && !todos.some((s) => s.id === g.parent_id),
  );

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Grupos</h2>
        <p className="text-body text-ink-2">
          Sistema e ambiente — RPA → prod. Cada monitor fica num ambiente (ou num sistema sem
          ambientes). O acesso das contas e os canais de aviso também podem ser dados por grupo.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {!scope.all_groups && (
        <p id={RESTRICAO_ID} className="max-w-prose text-small text-ink-3">
          Com acesso restrito a grupos, renomear um sistema — ou um ambiente cujo sistema não está
          no seu acesso — e vincular canais ficam com quem tem acesso a todos os grupos: o nome
          precisa ser conferido contra grupos que você não vê, e o canal avisa sobre os outros
          grupos também.
        </p>
      )}

      <NovoGrupo
        sistemas={sistemas}
        podeCriarSistema={podeCriarSistema}
        onCreated={async () => {
          setErro(null);
          await carregar();
        }}
        onError={setErro}
      />

      {grupos === null ? (
        <Loading what="grupos" />
      ) : grupos.length === 0 ? (
        <Nothing>Nenhum grupo. Os monitores ficam em "Sem grupo".</Nothing>
      ) : (
        <ul className="border-t border-line">
          {[...sistemas, ...soltos].map((s) => {
            const filhos = todos.filter((g) => g.parent_id === s.id);
            return (
              <li key={s.id}>
                <GroupRow
                  group={s}
                  groups={todos}
                  monitores={monitores.get(s.id) ?? 0}
                  filhos={filhos}
                  scope={scope}
                  onChanged={carregar}
                />
                {filhos.length > 0 && (
                  <ul>
                    {filhos.map((f) => (
                      <li key={f.id}>
                        <GroupRow
                          group={f}
                          groups={todos}
                          monitores={monitores.get(f.id) ?? 0}
                          filhos={[]}
                          scope={scope}
                          onChanged={carregar}
                        />
                      </li>
                    ))}
                  </ul>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

function NovoGrupo({
  sistemas,
  podeCriarSistema,
  onCreated,
  onError,
}: {
  sistemas: Group[];
  podeCriarSistema: boolean;
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const [nome, setNome] = useState("");
  const [pai, setPai] = useState("");

  // Sem poder criar sistema, o único destino possível é um sistema: a
  // opção "nenhum" ofereceria um botão que devolve "sem permissão".
  useEffect(() => {
    if (!podeCriarSistema && !pai && sistemas[0]) setPai(String(sistemas[0].id));
  }, [podeCriarSistema, pai, sistemas]);

  async function criar(e: FormEvent) {
    e.preventDefault();
    try {
      await api.createMonitorGroup({ name: nome, parent_id: pai ? Number(pai) : undefined });
      setNome("");
      await onCreated();
    } catch (e) {
      onError(e instanceof ApiError ? e.message : "Não foi possível criar o grupo.");
    }
  }

  return (
    <form
      onSubmit={criar}
      className="grid gap-4 rounded-sm border border-line-strong p-4 sm:grid-cols-[minmax(0,1fr)_200px_auto] sm:items-end"
    >
      <Field label="nome">
        <Input value={nome} onChange={(e) => setNome(e.target.value)} placeholder="prod" required />
      </Field>
      <Field label="dentro de">
        <Select value={pai} onChange={(e) => setPai(e.target.value)}>
          {podeCriarSistema && <option value="">— nenhum (é um sistema)</option>}
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
  );
}

/** RESTRICAO_ID liga os botões desligados à explicação da restrição. */
const RESTRICAO_ID = "grupos-restricao";

function plural(n: number, um: string, varios: string) {
  return `${n} ${n === 1 ? um : varios}`;
}

/**
 * GroupRow é um grupo com as três ações dele: avisos, renomear e apagar.
 * Cada uma abre no lugar, sob a linha, para a pessoa não perder de vista
 * de qual grupo está falando.
 */
function GroupRow({
  group,
  groups,
  monitores,
  filhos,
  scope,
  onChanged,
}: {
  group: Group;
  groups: Group[];
  monitores: number;
  filhos: Group[];
  scope: Scope;
  onChanged: () => Promise<void>;
}) {
  const [aberto, setAberto] = useState<"editar" | "avisos" | null>(null);
  const [recusa, setRecusa] = useState<string | null>(null);

  const ambiente = group.parent_id !== null;
  const nome = crumb(group.path);
  const podeRenomear = canRenameGroup(scope, group, groups);
  // Sem renomear, o formulário ainda serve para mudar de sistema — desde
  // que haja para onde ir e o grupo não tenha ambientes, que viraria o
  // terceiro nível.
  const podeMover =
    filhos.length === 0 && groups.some((g) => g.parent_id === null && g.id !== group.parent_id);
  const editavel = podeRenomear || (ambiente && podeMover);

  async function apagar() {
    setRecusa(null);
    if (filhos.length > 0) {
      setRecusa(
        `Não dá para apagar ${nome}: ainda tem ${plural(filhos.length, "ambiente", "ambientes")} (${filhos
          .map((f) => f.name)
          .join(", ")}). Apague ou mova os ambientes antes.`,
      );
      return;
    }
    if (monitores > 0) {
      setRecusa(
        `Não dá para apagar ${nome}: ainda tem ${plural(monitores, "monitor", "monitores")}. Mova-os para outro grupo ou remova-os antes.`,
      );
      return;
    }
    if (!confirm(`Apagar o grupo "${nome}"? As concessões de acesso a ele saem junto.`)) return;

    try {
      await api.deleteMonitorGroup(group.id);
      await onChanged();
    } catch (e) {
      setRecusa(e instanceof ApiError ? e.message : "Não foi possível apagar o grupo.");
    }
  }

  const resumo = [
    plural(monitores, "monitor", "monitores"),
    ...(filhos.length > 0 ? [plural(filhos.length, "ambiente", "ambientes")] : []),
  ].join(" · ");

  return (
    <div className={`flex flex-col gap-2 border-b border-line py-2.5 ${ambiente ? "pl-6" : ""}`}>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <span className="flex min-w-0 items-baseline gap-2">
          {ambiente && (
            <span aria-hidden className="text-ink-3">
              └
            </span>
          )}
          <span className={`truncate text-body ${ambiente ? "" : "font-medium"}`}>
            {ambiente ? group.name : group.path}
          </span>
          <span className="tabular text-small text-ink-3">{resumo}</span>
        </span>

        <span className="flex shrink-0 items-center gap-2">
          <Button
            size="sm"
            aria-expanded={aberto === "avisos"}
            onClick={() => setAberto(aberto === "avisos" ? null : "avisos")}
          >
            Avisos
          </Button>
          <Button
            size="sm"
            aria-expanded={aberto === "editar"}
            onClick={() => setAberto(aberto === "editar" ? null : "editar")}
            disabled={!editavel}
            aria-describedby={podeRenomear ? undefined : RESTRICAO_ID}
          >
            Renomear
          </Button>
          <Button size="sm" variant="danger" onClick={apagar}>
            Apagar
          </Button>
        </span>
      </div>

      {recusa && (
        <p role="alert" className="border-l-2 border-down bg-down-dim px-3 py-1.5 text-small text-ink">
          {recusa}
        </p>
      )}

      {aberto === "editar" && (
        <EditarGrupo
          group={group}
          groups={groups}
          temFilhos={filhos.length > 0}
          podeCriarSistema={scope.all_groups}
          podeRenomear={podeRenomear}
          onDone={async () => {
            setAberto(null);
            await onChanged();
          }}
          onCancel={() => setAberto(null)}
        />
      )}

      {aberto === "avisos" && <CanaisDoGrupo group={group} vincula={canLinkChannels(scope)} />}
    </div>
  );
}

function EditarGrupo({
  group,
  groups,
  temFilhos,
  podeCriarSistema,
  podeRenomear,
  onDone,
  onCancel,
}: {
  group: Group;
  groups: Group[];
  temFilhos: boolean;
  podeCriarSistema: boolean;
  podeRenomear: boolean;
  onDone: () => Promise<void>;
  onCancel: () => void;
}) {
  const [nome, setNome] = useState(group.name);
  const [pai, setPai] = useState(group.parent_id ? String(group.parent_id) : "");
  const [erro, setErro] = useState<string | null>(null);

  // Sistema com ambientes não desce de nível: viraria o terceiro nível.
  const podeMover = !temFilhos;
  const destinos = groups.filter((g) => g.parent_id === null && g.id !== group.id);

  async function salvar(e: FormEvent) {
    e.preventDefault();
    setErro(null);
    try {
      await api.updateMonitorGroup(group.id, {
        name: nome,
        parent_id: pai ? Number(pai) : undefined,
      });
      await onDone();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível salvar o grupo.");
    }
  }

  return (
    <form
      onSubmit={salvar}
      className="grid gap-3 rounded-sm border border-line-strong p-3 sm:grid-cols-[minmax(0,1fr)_200px_auto] sm:items-end"
    >
      <Field
        label="nome"
        error={erro ?? undefined}
        hint={podeRenomear ? undefined : "Renomear exige acesso a todos os grupos ao lado deste."}
      >
        <Input
          value={nome}
          onChange={(e) => setNome(e.target.value)}
          required
          autoFocus={podeRenomear}
          disabled={!podeRenomear}
        />
      </Field>
      <Field label="dentro de">
        <Select value={pai} onChange={(e) => setPai(e.target.value)} disabled={!podeMover}>
          {(podeCriarSistema || pai === "") && <option value="">— nenhum (é um sistema)</option>}
          {destinos.map((g) => (
            <option key={g.id} value={g.id}>
              {g.name}
            </option>
          ))}
        </Select>
      </Field>
      <span className="flex gap-2">
        <Button type="submit" variant="primary">
          Salvar
        </Button>
        <Button type="button" onClick={onCancel}>
          Cancelar
        </Button>
      </span>
    </form>
  );
}

/**
 * CanaisDoGrupo vincula canais ao grupo. Valem para todos os monitores
 * dele — num sistema, também para os dos ambientes —, inclusive os
 * cadastrados depois.
 *
 * Para a conta restrita é só leitura: o servidor recusa o vínculo, e a
 * lista de canais que ele lhe devolve vem recortada — não serve de
 * cardápio. Ela vê o que avisa sobre o grupo, e por que não muda.
 */
function CanaisDoGrupo({ group, vincula }: { group: Group; vincula: boolean }) {
  const [todos, setTodos] = useState<Channel[] | null>(null);
  const [vinculados, setVinculados] = useState<Set<number>>(new Set());
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const [lista, doGrupo] = await Promise.all([
      vincula ? api.listChannels() : Promise.resolve(null),
      api.groupChannels(group.id),
    ]);
    setTodos(lista ? lista.items : doGrupo.items);
    setVinculados(new Set(doGrupo.items.map((c) => c.id)));
  }, [group.id, vincula]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  async function alternar(canal: Channel) {
    const ligado = vinculados.has(canal.id);
    setVinculados((atual) => {
      const proximo = new Set(atual);
      if (ligado) proximo.delete(canal.id);
      else proximo.add(canal.id);
      return proximo;
    });
    setErro(null);
    try {
      if (ligado) await api.unlinkGroupChannel(group.id, canal.id);
      else await api.linkGroupChannel(group.id, canal.id);
    } catch (e) {
      // A caixa volta ao que o servidor tem, e a recusa fica à vista: sem
      // ela, o clique pareceria não ter pegado.
      setErro(e instanceof ApiError ? e.message : "Não foi possível mudar o vínculo.");
      await carregar();
    }
  }

  if (todos === null) return <Loading what="canais" />;

  const titulo = (
    <span className="eyebrow">
      avisam sobre todos os monitores de {crumb(group.path)}
      {group.parent_id === null ? " e dos ambientes dele" : ""}
    </span>
  );

  if (!vincula) {
    return (
      <div className="flex flex-col gap-1 rounded-sm border border-line-strong p-3">
        {titulo}
        {todos.length === 0 ? (
          <p className="text-small text-ink-2">Nenhum canal vinculado a este grupo.</p>
        ) : (
          <ul className="flex flex-col">
            {todos.map((canal) => (
              <li key={canal.id} className="flex items-baseline gap-2 py-1">
                <span className="text-body">{canal.name}</span>
                <span className="eyebrow">{channelTypeLabel(canal.type)}</span>
                {!canal.enabled && <span className="text-small text-ink-3">desligado</span>}
              </li>
            ))}
          </ul>
        )}
        <p className="max-w-prose text-small text-ink-3">{NO_CHANNEL_LINK_HINT}</p>
      </div>
    );
  }

  if (todos.length === 0) {
    return <p className="text-small text-ink-3">Nenhum canal cadastrado ainda.</p>;
  }

  return (
    <div className="flex flex-col gap-1 rounded-sm border border-line-strong p-3">
      {titulo}
      {erro && <Alert>{erro}</Alert>}
      <ul className="-mx-1 flex flex-col">
        {todos.map((canal) => (
          <li key={canal.id}>
            <Checkbox checked={vinculados.has(canal.id)} onChange={() => alternar(canal)}>
              <span>{canal.name}</span>
              <span className="eyebrow">{channelTypeLabel(canal.type)}</span>
              {!canal.enabled && <span className="text-small text-ink-3">desligado</span>}
            </Checkbox>
          </li>
        ))}
      </ul>
    </div>
  );
}
