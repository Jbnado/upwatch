import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type APIToken } from "../api/types";
import { Alert, Button, Empty, Field, Input } from "../components/ui";
import { ago, stamp } from "../lib/format";
import { navigate } from "../lib/router";

/** Ajustes: credenciais de acesso programático e troca de senha. */
export function Settings({ onSignedOut }: { onSignedOut: () => void }) {
  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-10 px-5 py-6">
      <nav>
        <button onClick={() => navigate({ name: "board" })} className="eyebrow hover:text-ink">
          ← todos os monitores
        </button>
      </nav>

      <h1 className="text-[22px] font-semibold tracking-tight">Ajustes</h1>

      <Tokens />
      <Password onSignedOut={onSignedOut} />
    </div>
  );
}

function Tokens() {
  const [tokens, setTokens] = useState<APIToken[] | null>(null);
  const [nome, setNome] = useState("");
  const [novo, setNovo] = useState<string | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  async function carregar() {
    const { items } = await api.listTokens();
    setTokens(items);
  }

  useEffect(() => {
    void carregar();
  }, []);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setErro(null);

    try {
      const criado = await api.createToken(nome);
      setNovo(criado.token);
      setNome("");
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível criar o token.");
    }
  }

  async function revogar(token: APIToken) {
    if (!confirm(`Revogar "${token.name}"? Quem estiver usando perde o acesso na hora.`)) return;

    await api.revokeToken(token.id);
    await carregar();
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <h2 className="text-[15px] font-medium">Tokens de acesso</h2>
        <p className="text-[13px] text-ink-2">
          Para scripts e integrações. Um token faz tudo o que esta interface faz.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {/* O segredo aparece uma única vez. Deixar isso explícito evita que
          alguém feche a tela e perca a credencial recém-criada. */}
      {novo && (
        <div className="flex flex-col gap-2 border-l-2 border-degraded bg-degraded-dim px-3 py-2.5">
          <p className="text-[13px] font-medium">Copie agora. Este valor não aparece de novo.</p>
          <code className="block overflow-x-auto rounded-[3px] border border-line-strong bg-surface px-2.5 py-1.5 text-[12px]">
            {novo}
          </code>
          <button
            onClick={() => setNovo(null)}
            className="eyebrow self-start hover:text-ink"
          >
            já guardei
          </button>
        </div>
      )}

      <form onSubmit={criar} className="flex items-end gap-2">
        <div className="flex-1">
          <Field label="novo token" hint="Um nome que diga onde ele é usado.">
            <Input
              value={nome}
              onChange={(e) => setNome(e.target.value)}
              placeholder="pipeline de deploy"
              required
            />
          </Field>
        </div>
        <Button type="submit" variant="primary">
          Criar
        </Button>
      </form>

      {tokens === null ? (
        <p className="eyebrow">carregando</p>
      ) : tokens.length === 0 ? (
        <Empty
          title="Nenhum token criado"
          description="Crie um quando precisar cadastrar monitores ou consultar histórico por script."
        />
      ) : (
        <ul className="border-t border-line">
          {tokens.map((token) => (
            <li
              key={token.id}
              className="flex items-baseline justify-between gap-4 border-b border-line py-2.5"
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate text-[13px] font-medium">{token.name}</span>
                <span className="tabular text-[12px] text-ink-3">{token.prefix}…</span>
              </span>

              <span className="flex shrink-0 items-baseline gap-4">
                <span className="text-[12px] text-ink-3">
                  {token.last_used_at ? `usado ${ago(token.last_used_at)}` : "nunca usado"}
                  <span className="mx-1.5">·</span>
                  criado em {stamp(token.created_at)}
                </span>
                <Button variant="danger" onClick={() => revogar(token)}>
                  Revogar
                </Button>
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Password({ onSignedOut }: { onSignedOut: () => void }) {
  const [atual, setAtual] = useState("");
  const [nova, setNova] = useState("");
  const [erro, setErro] = useState<string | null>(null);

  async function trocar(e: FormEvent) {
    e.preventDefault();
    setErro(null);

    try {
      await api.changePassword(atual, nova);
      // A troca encerra todas as sessões, inclusive esta — é o objetivo,
      // não um efeito colateral.
      onSignedOut();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível trocar a senha.");
    }
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1">
        <h2 className="text-[15px] font-medium">Senha</h2>
        <p className="text-[13px] text-ink-2">
          Trocar a senha encerra todas as sessões abertas, inclusive esta.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <form onSubmit={trocar} className="flex max-w-sm flex-col gap-4">
        <Field label="senha atual">
          <Input
            type="password"
            value={atual}
            onChange={(e) => setAtual(e.target.value)}
            autoComplete="current-password"
            required
          />
        </Field>

        <Field label="nova senha" hint="No mínimo 12 caracteres.">
          <Input
            type="password"
            value={nova}
            onChange={(e) => setNova(e.target.value)}
            autoComplete="new-password"
            required
          />
        </Field>

        <Button type="submit" variant="primary" className="self-start">
          Trocar senha
        </Button>
      </form>
    </section>
  );
}
