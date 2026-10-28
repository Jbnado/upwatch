import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Group, type Role, type Scope, type User } from "../api/types";
import { CheckTree, type TreeItem } from "../components/CheckTree";
import {
  Alert,
  Button,
  Field,
  FormActions,
  Input,
  Loading,
  NewItem,
  Select,
} from "../components/ui";
import { stamp } from "../lib/format";
import { crumb } from "../lib/groups";
import { covers, scopeLabel, toggleScopeGroup } from "../lib/scope";

/**
 * Contas de acesso.
 *
 * Dois papéis, não uma matriz de permissões: a pergunta que um time faz
 * sobre uma conta é "essa pessoa pode mexer?". Uma lista de caixinhas
 * por recurso viraria configuração que ninguém revisa — e que, por isso,
 * acaba com todo mundo marcado. O que varia é o alcance: todos os grupos
 * ou uma lista deles.
 *
 * A tela só aparece para quem administra, mas isso é conveniência: a
 * barreira de verdade está no servidor, porque botão escondido não é
 * permissão.
 */

const PAPEIS: { value: Role; label: string; ajuda: string }[] = [
  { value: "admin", label: "Administrador", ajuda: "Cadastra alvos, canais, páginas e contas." },
  { value: "viewer", label: "Observador", ajuda: "Vê o que o acesso permite, não altera nada." },
];

/** scopeOK diz se o escopo pode ir ao servidor: lista vazia é recusada. */
function scopeOK(s: Scope) {
  return s.all_groups || s.group_ids.length > 0;
}

export function Users({ eu }: { eu: User }) {
  const [contas, setContas] = useState<User[] | null>(null);
  const [grupos, setGrupos] = useState<Group[]>([]);
  const [erro, setErro] = useState<string | null>(null);
  const [editando, setEditando] = useState<number | null>(null);

  const carregar = useCallback(async () => {
    const [{ items }, g] = await Promise.all([api.listUsers(), api.listGroups()]);
    setContas(items);
    setGrupos(g.items);
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  async function trocarPapel(u: User, papel: Role) {
    setErro(null);
    try {
      await api.setUserRole(u.id, papel);
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível trocar o papel.");
    }
  }

  async function trocarEscopo(u: User, escopo: Scope) {
    setErro(null);
    try {
      await api.setUserScope(u.id, u.role, escopo);
      setEditando(null);
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível trocar o acesso.");
    }
  }

  async function remover(u: User) {
    const aviso =
      u.id === eu.id
        ? `Remover a sua própria conta "${u.username}"? Você perde o acesso imediatamente.`
        : `Remover "${u.username}"? A sessão dela é encerrada na hora.`;
    if (!confirm(aviso)) return;

    setErro(null);
    try {
      await api.deleteUser(u.id);
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível remover a conta.");
    }
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Contas e acesso</h2>
        <p className="text-body text-ink-2">
          Quem entra na interface e o que enxerga. Observador vê os monitores do acesso dele,
          inclusive endereço de alvo e causa de falha — é uma conta de dentro, não a página
          pública.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {contas === null ? (
        <Loading what="contas" />
      ) : (
        <ul className="border-t border-line">
          {contas.map((u) => (
            <li key={u.id} className="flex flex-col gap-3 border-b border-line py-2.5">
              <div className="flex flex-wrap items-center justify-between gap-4">
                <span className="flex min-w-0 flex-col">
                  <span className="flex items-baseline gap-2">
                    <span className="truncate text-body font-medium">{u.username}</span>
                    {u.id === eu.id && <span className="eyebrow">você</span>}
                  </span>
                  <span className="text-small text-ink-3">
                    vê <span className="text-ink-2">{scopeLabel(u.scope, grupos)}</span>
                    <span className="mx-1.5">·</span>
                    criada em <span className="tabular">{stamp(u.created_at)}</span>
                  </span>
                </span>

                {/* Encolhe no celular: com shrink-0 os três controles
                    somavam mais que a tela e o Remover saía pela borda. */}
                <span className="flex min-w-0 max-w-full items-center gap-2">
                  <Button
                    size="sm"
                    aria-expanded={editando === u.id}
                    onClick={() => setEditando(editando === u.id ? null : u.id)}
                  >
                    Alterar acesso
                  </Button>
                  {/* O papel se troca no lugar, sem tela de edição: abrir
                      uma tela para um campo só é um clique a mais sem nada
                      em troca. */}
                  {/* A largura vai no invólucro: o campo é w-full por
                      padrão, e as duas larguras na mesma classe brigavam. */}
                  <span className="min-w-0 flex-1 sm:w-[168px] sm:flex-none">
                    <Select
                      value={u.role}
                      onChange={(e) => trocarPapel(u, e.target.value as Role)}
                      aria-label={`papel de ${u.username}`}
                    >
                      {PAPEIS.map((p) => (
                        <option key={p.value} value={p.value}>
                          {p.label}
                        </option>
                      ))}
                    </Select>
                  </span>
                  <Button size="sm" variant="danger" onClick={() => remover(u)}>
                    Remover
                  </Button>
                </span>
              </div>

              {editando === u.id && (
                <EditarEscopo
                  conta={u}
                  grupos={grupos}
                  podeTodos={eu.scope.all_groups}
                  onSave={(escopo) => trocarEscopo(u, escopo)}
                  onCancel={() => setEditando(null)}
                />
              )}
            </li>
          ))}
        </ul>
      )}

      <NewItem label="Nova conta">
        {(fechar) => (
          <NovaConta
            onCreated={async () => {
              fechar();
              await carregar();
            }}
            onError={setErro}
            onCancel={fechar}
            grupos={grupos}
            podeTodos={eu.scope.all_groups}
          />
        )}
      </NewItem>
    </section>
  );
}

/**
 * ScopePicker escolhe o que a conta enxerga: todos os grupos, ou uma
 * lista. Marcar o sistema marca os ambientes dele, que aparecem marcados
 * e travados — desmarcar um ambiente coberto pelo sistema não teria
 * efeito, e uma caixa que não obedece ensina a desconfiar das outras.
 */
export function ScopePicker({
  value,
  onChange,
  groups,
  podeTodos,
  name,
}: {
  value: Scope;
  onChange: (s: Scope) => void;
  groups: Group[];
  /** Quem tem escopo não concede todos os grupos; o servidor recusaria. */
  podeTodos: boolean;
  /** Nome do grupo de rádios, único na tela. */
  name: string;
}) {
  const sistemas = groups.filter((g) => g.parent_id === null);
  const soltos = groups.filter(
    (g) => g.parent_id !== null && !groups.some((s) => s.id === g.parent_id),
  );

  const marcar = (key: string) =>
    onChange({
      all_groups: false,
      group_ids: toggleScopeGroup(value.group_ids, Number(key), groups),
    });

  const item = (g: Group, recuo: boolean): TreeItem => {
    const peloSistema = !value.group_ids.includes(g.id) && covers(value.group_ids, g);
    return {
      key: String(g.id),
      label: recuo ? g.name : crumb(g.path),
      state: covers(value.group_ids, g) ? "checked" : "unchecked",
      disabled: peloSistema,
      meta: peloSistema ? "pelo sistema" : undefined,
    };
  };

  const itens: TreeItem[] = [
    ...sistemas.map((s) => ({
      ...item(s, false),
      children: groups.filter((g) => g.parent_id === s.id).map((g) => item(g, true)),
    })),
    ...soltos.map((g) => item(g, false)),
  ];

  return (
    <div className="flex flex-col gap-2">
      {podeTodos && (
        <div role="radiogroup" aria-label="alcance" className="flex flex-wrap gap-x-4 gap-y-1">
          <label className="flex cursor-pointer items-center gap-2 text-body">
            <input
              type="radio"
              name={name}
              className="accent-ink"
              checked={value.all_groups}
              onChange={() => onChange({ all_groups: true, group_ids: [] })}
            />
            Todos os grupos
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-body">
            <input
              type="radio"
              name={name}
              className="accent-ink"
              checked={!value.all_groups}
              onChange={() => onChange({ all_groups: false, group_ids: value.group_ids })}
            />
            Só os grupos escolhidos
          </label>
        </div>
      )}

      {!value.all_groups &&
        (groups.length === 0 ? (
          <p className="text-small text-ink-3">Nenhum grupo cadastrado ainda.</p>
        ) : (
          <CheckTree items={itens} onToggle={marcar} />
        ))}

      {!scopeOK(value) && groups.length > 0 && (
        <span className="text-small text-ink-3">Escolha pelo menos um grupo.</span>
      )}
    </div>
  );
}

function EditarEscopo({
  conta,
  grupos,
  podeTodos,
  onSave,
  onCancel,
}: {
  conta: User;
  grupos: Group[];
  podeTodos: boolean;
  onSave: (s: Scope) => void;
  onCancel: () => void;
}) {
  const [escopo, setEscopo] = useState<Scope>(conta.scope);

  return (
    <div className="flex flex-col gap-3 rounded-sm border border-line-strong p-3">
      <span className="eyebrow">o que {conta.username} enxerga</span>
      <ScopePicker
        value={escopo}
        onChange={setEscopo}
        groups={grupos}
        podeTodos={podeTodos}
        name={`escopo-${conta.id}`}
      />
      <span className="flex gap-2">
        <Button variant="primary" disabled={!scopeOK(escopo)} onClick={() => onSave(escopo)}>
          Salvar acesso
        </Button>
        <Button onClick={onCancel}>Cancelar</Button>
      </span>
    </div>
  );
}

function NovaConta({
  onCreated,
  onError,
  onCancel,
  grupos,
  podeTodos,
}: {
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
  onCancel: () => void;
  grupos: Group[];
  podeTodos: boolean;
}) {
  const inicial: Scope = { all_groups: podeTodos, group_ids: [] };

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [escopo, setEscopo] = useState<Scope>(inicial);
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setCampoErro(null);

    try {
      await api.createUser({ username, password, role, scope: escopo });
      setUsername("");
      setPassword("");
      setRole("viewer");
      setEscopo(inicial);
      await onCreated();
    } catch (e) {
      if (e instanceof ApiError && e.field) setCampoErro({ campo: e.field, msg: e.message });
      else if (e instanceof ApiError) onError(e.message);
      else onError("Não foi possível criar a conta.");
    } finally {
      setEnviando(false);
    }
  }

  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);
  const papel = PAPEIS.find((p) => p.value === role)!;

  return (
    <form onSubmit={criar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_180px]">
        <Field label="usuário" error={erroDe("username")}>
          <Input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="ana"
            autoComplete="off"
            autoFocus
            required
          />
        </Field>

        <Field label="senha" hint="No mínimo 12 caracteres." error={erroDe("password")}>
          <Input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>

        {/* Observador é o padrão do formulário. O papel que menos
            incomoda seria administrador, e é justamente por isso que ele
            não pode ser o padrão. */}
        <Field label="papel" hint={papel.ajuda} error={erroDe("role")}>
          <Select value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {PAPEIS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      {/* fieldset e não Field: são várias caixas, e um label em volta de
          todas faria um clique no texto marcar a primeira. */}
      <fieldset className="flex flex-col gap-1.5">
        <legend className="eyebrow mb-1.5">acesso</legend>
        <ScopePicker
          value={escopo}
          onChange={setEscopo}
          groups={grupos}
          podeTodos={podeTodos}
          name="escopo-nova-conta"
        />
        {erroDe("scope") ? (
          <span className="text-small text-down">{erroDe("scope")}</span>
        ) : (
          <span className="text-small text-ink-3">
            Dar acesso a um sistema dá acesso aos ambientes dele, inclusive os criados depois.
          </span>
        )}
      </fieldset>

      <FormActions
        submit={enviando ? "Criando" : "Criar conta"}
        busy={enviando}
        disabled={!scopeOK(escopo)}
        onCancel={onCancel}
      />
    </form>
  );
}
