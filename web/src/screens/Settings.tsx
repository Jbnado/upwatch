import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type APIToken } from "../api/types";
import { Alert, Button, Field, Input, Loading, Nothing, TextLink } from "../components/ui";
import { ago, stamp } from "../lib/format";
import { navigate } from "../lib/router";
import { Channels } from "./Channels";

/** Ajustes: credenciais de acesso programático e troca de senha. */
export function Settings({ onSignedOut }: { onSignedOut: () => void }) {
  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-10 px-5 py-6">
      {/* Navegação e título formam um bloco. Soltos no ritmo de 40px que
          separa as seções, ficavam a quatro dedos de distância um do
          outro — como se fossem assuntos diferentes. */}
      <div className="flex flex-col gap-6">
        <nav>
          <TextLink onClick={() => navigate({ name: "board" })}>← todos os monitores</TextLink>
        </nav>
        <h1 className="text-title font-semibold tracking-tight">Ajustes</h1>
      </div>

      <Channels />
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
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Tokens de acesso</h2>
        <p className="text-body text-ink-2">
          Para scripts e integrações. Um token faz tudo o que esta interface faz.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {/* O segredo aparece uma única vez. Deixar isso explícito evita que
          alguém feche a tela e perca a credencial recém-criada. */}
      {novo && (
        <div className="flex flex-col gap-2 border-l-2 border-degraded bg-degraded-dim px-3 py-2.5">
          <p className="text-body font-medium">Copie agora. Este valor não aparece de novo.</p>
          <code className="block overflow-x-auto rounded-sm border border-line-strong bg-surface px-2.5 py-1.5 text-small">
            {novo}
          </code>
          <TextLink onClick={() => setNovo(null)} className="self-start">
            já guardei
          </TextLink>
        </div>
      )}

      {/* Campo e botão dividem uma linha própria, e a dica fica sob os
          dois. Empilhar o Field inteiro ao lado do botão alinhava o botão
          com a base da dica — ele descia uma linha e ficava torto em
          relação ao campo que o acompanha. */}
      <form onSubmit={criar} className="flex flex-col gap-1.5">
        <label htmlFor="novo-token" className="eyebrow">
          novo token
        </label>
        <div className="flex gap-2">
          <Input
            id="novo-token"
            value={nome}
            onChange={(e) => setNome(e.target.value)}
            placeholder="pipeline de deploy"
            className="flex-1"
            required
          />
          <Button type="submit" variant="primary" className="shrink-0">
            Criar
          </Button>
        </div>
        <span className="text-small text-ink-3">Um nome que diga onde ele é usado.</span>
      </form>

      {tokens === null ? (
        <Loading what="tokens" />
      ) : tokens.length === 0 ? (
        <Nothing hint="Crie um quando precisar cadastrar monitores ou consultar histórico por script.">
          Nenhum token criado.
        </Nothing>
      ) : (
        <ul className="border-t border-line">
          {tokens.map((token) => (
            <li
              key={token.id}
              // items-center e não items-baseline: a coluna da esquerda tem
              // duas linhas, e alinhar pela primeira delas deixaria o botão
              // da direita flutuando acima do centro da linha.
              className="flex items-center justify-between gap-4 border-b border-line py-2.5"
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate text-body font-medium">{token.name}</span>
                <span className="tabular text-small text-ink-3">{token.prefix}…</span>
              </span>

              <span className="flex shrink-0 items-center gap-4">
                <span className="text-small text-ink-3">
                  {token.last_used_at ? `usado ${ago(token.last_used_at)}` : "nunca usado"}
                  <span className="mx-1.5">·</span>
                  criado em {stamp(token.created_at)}
                </span>
                <Button size="sm" variant="danger" onClick={() => revogar(token)}>
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
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Senha</h2>
        <p className="text-body text-ink-2">
          Trocar a senha encerra todas as sessões abertas, inclusive esta.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {/* Mesma largura das outras seções: três blocos com três bordas
          direitas diferentes deixam a página com a margem serrilhada. */}
      <form onSubmit={trocar} className="flex flex-col gap-4">
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
