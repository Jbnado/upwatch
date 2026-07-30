import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Role, type User } from "../api/types";
import { Alert, Button, Field, Input, Loading, Select } from "../components/ui";
import { stamp } from "../lib/format";

/**
 * Contas de acesso.
 *
 * Dois papéis, não uma matriz de permissões: a pergunta que um time faz
 * sobre uma conta é "essa pessoa pode mexer?". Uma lista de caixinhas
 * por recurso viraria configuração que ninguém revisa — e que, por isso,
 * acaba com todo mundo marcado.
 *
 * A tela só aparece para quem administra, mas isso é conveniência: a
 * barreira de verdade está no servidor, porque botão escondido não é
 * permissão.
 */

const PAPEIS: { value: Role; label: string; ajuda: string }[] = [
  { value: "admin", label: "Administrador", ajuda: "Cadastra alvos, canais, páginas e contas." },
  { value: "viewer", label: "Observador", ajuda: "Vê tudo, não altera nada." },
];

export function Users({ eu }: { eu: User }) {
  const [contas, setContas] = useState<User[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const { items } = await api.listUsers();
    setContas(items);
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
        <h2 className="text-lead font-medium">Contas</h2>
        <p className="text-body text-ink-2">
          Quem entra na interface. Observador vê tudo o que você vê, inclusive endereço de alvo e
          causa de falha — é uma conta de dentro, não a página pública.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <NovaConta onCreated={carregar} onError={setErro} />

      {contas === null ? (
        <Loading what="contas" />
      ) : (
        <ul className="border-t border-line">
          {contas.map((u) => (
            <li
              key={u.id}
              className="flex flex-wrap items-center justify-between gap-4 border-b border-line py-2.5"
            >
              <span className="flex min-w-0 flex-col">
                <span className="flex items-baseline gap-2">
                  <span className="truncate text-body font-medium">{u.username}</span>
                  {u.id === eu.id && <span className="eyebrow">você</span>}
                </span>
                <span className="text-small text-ink-3">
                  criada em <span className="tabular">{stamp(u.created_at)}</span>
                </span>
              </span>

              <span className="flex shrink-0 items-center gap-2">
                {/* O papel se troca no lugar, sem tela de edição: é o
                    único campo editável, e abrir uma tela para um campo
                    só é um clique a mais sem nada em troca. */}
                <Select
                  value={u.role}
                  onChange={(e) => trocarPapel(u, e.target.value as Role)}
                  className="w-[168px]"
                  aria-label={`papel de ${u.username}`}
                >
                  {PAPEIS.map((p) => (
                    <option key={p.value} value={p.value}>
                      {p.label}
                    </option>
                  ))}
                </Select>
                <Button size="sm" variant="danger" onClick={() => remover(u)}>
                  Remover
                </Button>
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function NovaConta({
  onCreated,
  onError,
}: {
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setCampoErro(null);

    try {
      await api.createUser({ username, password, role });
      setUsername("");
      setPassword("");
      setRole("viewer");
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

      <Button type="submit" variant="primary" disabled={enviando} className="self-start">
        {enviando ? "Criando" : "Criar conta"}
      </Button>
    </form>
  );
}
