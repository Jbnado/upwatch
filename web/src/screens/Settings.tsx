import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type APIToken, type User } from "../api/types";
import {
  Alert,
  Button,
  Field,
  FormActions,
  Input,
  Loading,
  NewItem,
  Nothing,
  RowLink,
  TextLink,
} from "../components/ui";
import { ago, stamp } from "../lib/format";
import { href, navigate } from "../lib/router";
import {
  firstSection,
  sectionAccess,
  sectionLabel,
  settingsMenu,
  type SectionId,
} from "../lib/settings-sections";
import { Channels } from "./Channels";
import { Groups } from "./Groups";
import { StatusPageAdmin, StatusPages } from "./StatusPages";
import { Users } from "./Users";

/**
 * Ajustes: um menu lateral e uma seção por vez.
 *
 * Empilhadas numa página só, as seções faziam de "trocar a senha" uma
 * rolagem por cima de canais, grupos e contas — e o endereço não levava a
 * lugar nenhum dentro dela. Agora cada seção tem endereço próprio, e o
 * link de "crie um canal em ajustes" cai nos canais.
 *
 * No celular o menu e a seção não cabem lado a lado: /ajustes é o menu, e
 * a seção ocupa a tela com a volta à vista. No computador, /ajustes abre a
 * primeira seção da conta ao lado do menu.
 */
export function Settings({
  eu,
  section,
  pageId,
  onSignedOut,
}: {
  eu: User;
  section?: SectionId;
  /** Página de estado em edição, dentro da seção de páginas. */
  pageId?: number;
  onSignedOut: () => void;
}) {
  const escolhida = section ?? (pageId ? "paginas" : undefined);
  const aberta = escolhida ?? firstSection(eu);
  const acesso = sectionAccess(eu, aberta);

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-6 px-5 py-6">
      {/* Navegação e título formam um bloco: no celular, dentro de uma
          seção, a volta é para o menu; no menu, para o painel. */}
      <div className={`flex flex-col gap-6 ${escolhida ? "max-md:hidden" : ""}`}>
        <nav aria-label="Voltar">
          <TextLink onClick={() => navigate({ name: "board" })}>← todos os monitores</TextLink>
        </nav>
        <h1 className="text-title font-semibold tracking-tight">Ajustes</h1>
      </div>

      <div className="flex flex-col gap-6 md:grid md:grid-cols-[13rem_minmax(0,1fr)] md:gap-10">
        <Menu eu={eu} current={escolhida} aberta={aberta} />

        <div className={`flex min-w-0 max-w-2xl flex-col gap-6 ${escolhida ? "" : "max-md:hidden"}`}>
          {/* Na edição de uma página a volta é para a lista de páginas,
              que a própria tela oferece; duas voltas empilhadas competiam. */}
          <nav aria-label="Voltar ao menu" className={pageId ? "hidden" : "md:hidden"}>
            <a
              href={href({ name: "ajustes" })}
              onClick={(e) => {
                e.preventDefault();
                navigate({ name: "ajustes" });
              }}
              className="pressable text-action eyebrow hover:text-ink hover:underline active:text-ink-2"
            >
              ← Ajustes
            </a>
          </nav>

          {acesso.ok ? (
            <Secao id={aberta} pageId={pageId} eu={eu} onSignedOut={onSignedOut} />
          ) : (
            <SemAcesso titulo={sectionLabel(aberta)} motivo={acesso.reason} />
          )}
        </div>
      </div>
    </div>
  );
}

/**
 * Menu é a lista de seções da conta.
 *
 * Links de verdade, não botões: abrir uma seção em outra aba com Ctrl e
 * copiar o endereço dela é o que se espera de um menu de navegação.
 */
function Menu({
  eu,
  current,
  aberta,
}: {
  eu: User;
  /** Seção do endereço; vazia no menu do celular. */
  current?: SectionId;
  /** Seção que aparece ao lado no computador. */
  aberta: SectionId;
}) {
  return (
    <nav
      aria-label="Seções dos ajustes"
      className={`flex flex-col gap-5 ${current ? "max-md:hidden" : ""} md:sticky md:top-16 md:self-start`}
    >
      {settingsMenu(eu).map((grupo) => (
        <div key={grupo.title} className="flex flex-col gap-1">
          <span id={`menu-${grupo.title}`} className="eyebrow px-2.5">
            {grupo.title}
          </span>
          <ul aria-labelledby={`menu-${grupo.title}`} className="flex flex-col max-md:border-t max-md:border-line">
            {grupo.items.map((item) => {
              const atual = item.id === aberta;
              return (
                <li key={item.id}>
                  <RowLink
                    to={{ name: "ajustes", section: item.id }}
                    current={atual}
                    className={[
                      "flex items-center justify-between gap-2 rounded-sm px-2.5 text-body",
                      // No celular o item é linha de lista, alto o bastante
                      // para o dedo, e nenhum está "aberto": o menu ocupa a
                      // tela sozinho. No computador, um degrau compacto com
                      // a seção ao lado marcada.
                      "max-md:rounded-none max-md:border-b max-md:border-line max-md:py-3 max-md:text-ink",
                      "md:py-1.5",
                      atual
                        ? "md:bg-sunken md:font-medium md:text-ink md:shadow-[inset_2px_0_0_var(--color-ink)]"
                        : "md:text-ink-2 md:hover:text-ink",
                    ].join(" ")}
                  >
                    <span>{item.label}</span>
                    <span aria-hidden className="text-ink-3 md:hidden">
                      ›
                    </span>
                  </RowLink>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </nav>
  );
}

function Secao({
  id,
  pageId,
  eu,
  onSignedOut,
}: {
  id: SectionId;
  pageId?: number;
  eu: User;
  onSignedOut: () => void;
}) {
  switch (id) {
    case "grupos":
      return <Groups scope={eu.scope} />;
    case "contas":
      return <Users eu={eu} />;
    case "canais":
      return <Channels />;
    case "paginas":
      return pageId ? <StatusPageAdmin id={pageId} /> : <StatusPages />;
    case "tokens":
      return <Tokens />;
    case "senha":
      return <Password onSignedOut={onSignedOut} />;
  }
}

/**
 * SemAcesso é a seção que a conta abriu por um link e não pode usar.
 * Diz o porquê: uma tela vazia, ou uma lista onde tudo devolve "sem
 * permissão", deixaria a pessoa achando que algo quebrou.
 */
function SemAcesso({ titulo, motivo }: { titulo: string; motivo: string }) {
  return (
    <section className="flex flex-col gap-1.5">
      <h2 className="text-lead font-medium">{titulo}</h2>
      <p className="max-w-prose border-l-2 border-line-strong pl-3 text-body text-ink-2">{motivo}</p>
    </section>
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

  /** criar devolve se deu certo, para o formulário se recolher só então. */
  async function criar(e: FormEvent): Promise<boolean> {
    e.preventDefault();
    setErro(null);

    try {
      const criado = await api.createToken(nome);
      setNovo(criado.token);
      setNome("");
      await carregar();
      return true;
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível criar o token.");
      return false;
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
        <h2 className="text-lead font-medium">Tokens de API</h2>
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

      <NewItem label="Novo token">
        {(fechar) => (
          <form
            onSubmit={async (e) => {
              if (await criar(e)) fechar();
            }}
            className="flex flex-col gap-4 rounded-sm border border-line-strong p-4"
          >
            <Field label="nome" hint="Um nome que diga onde ele é usado.">
              <Input
                value={nome}
                onChange={(e) => setNome(e.target.value)}
                placeholder="pipeline de deploy"
                autoFocus
                required
              />
            </Field>
            <FormActions submit="Criar token" onCancel={fechar} />
          </form>
        )}
      </NewItem>
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
