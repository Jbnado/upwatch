import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { GroupSummary, Problem } from "../api/types";
import { Alert, Button, Empty, Loading, RowLink, StatusDot } from "../components/ui";
import { explainCause } from "../lib/cause";
import { elapsed } from "../lib/format";
import { problemRoute } from "../lib/events";
import { cardWord, crumb, hasGroups, organizeCards, problemWord } from "../lib/groups";
import { href, navigate } from "../lib/router";
import { Board, Summary } from "./Board";

/**
 * A tela inicial.
 *
 * Duas coisas e nada mais: o que está errado agora e um cartão por
 * ambiente. Sem lista nem gráfico embaixo — quem abre o painel quer saber
 * se precisa agir, e a resposta cabe numa olhada. O detalhe mora a um
 * clique, no cartão ou no problema.
 *
 * Instalação sem grupos cai na lista de sempre: um cartão solitário de
 * "Sem grupo" seria só um clique a mais até o mesmo lugar.
 */
export function Home({ podeEscrever }: { podeEscrever: boolean }) {
  const [cartoes, setCartoes] = useState<GroupSummary[] | null>(null);
  const [problemas, setProblemas] = useState<Problem[]>([]);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const [resumo, agora] = await Promise.all([api.groupSummary(), api.problems()]);
      setCartoes(resumo.items);
      setProblemas(agora.items);
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar o painel.");
    }
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  // Mesma cadência da lista: durante um incidente ninguém quer apertar
  // atualizar para saber se voltou.
  useEffect(() => {
    const id = setInterval(() => void carregar(), 30_000);
    return () => clearInterval(id);
  }, [carregar]);

  if (cartoes === null) {
    return (
      <div className="mx-auto max-w-6xl px-5 py-6">
        {erro ? <Alert>{erro}</Alert> : <Loading what="o painel" />}
      </div>
    );
  }

  if (!hasGroups(cartoes)) {
    // A conta com escopo nunca vê "Sem grupo"; para ela, nada aqui quer
    // dizer que nenhum grupo foi concedido, e não que não há monitores.
    if (cartoes.length === 0 && !podeEscrever) return <SemAcesso />;
    return <Board podeEscrever={podeEscrever} />;
  }

  const secoes = organizeCards(cartoes);
  const contagem = secoes
    .flatMap((s) => s.cards)
    .reduce(
      (acc, c) => ({
        up: acc.up + c.up,
        degraded: acc.degraded + c.degraded,
        down: acc.down + c.down,
        unknown: acc.unknown + c.unknown,
        paused: acc.paused + c.paused,
        total: acc.total + c.total,
      }),
      { up: 0, degraded: 0, down: 0, unknown: 0, paused: 0, total: 0 },
    );

  return (
    <div className="mx-auto flex max-w-6xl flex-col gap-10 px-5 py-6">
      <header className="flex flex-wrap items-baseline justify-between gap-4">
        <Summary {...contagem} />
        {podeEscrever && (
          <Button variant="primary" onClick={() => navigate({ name: "monitor-new" })}>
            Novo monitor
          </Button>
        )}
      </header>

      {erro && <Alert>{erro}</Alert>}

      {problemas.length > 0 && <ProblemsNow items={problemas} />}

      <div className="flex flex-wrap gap-x-10 gap-y-8">
        {secoes.map((s) => (
          <section
            key={s.title || "sem-grupo"}
            aria-label={s.title || "Sem grupo"}
            className="flex w-full flex-col gap-2 sm:w-auto"
          >
            {/* O título fica mesmo vazio, como espaço: sem ele o cartão
                de "Sem grupo" subiria meia linha e sairia do alinhamento
                dos outros. */}
            <h2 className="eyebrow h-4">{s.title}</h2>
            <ul className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
              {s.cards.map((c) => (
                <li key={c.group_id ?? "sem-grupo"}>
                  <GroupCard card={c} system={s.title} />
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </div>
  );
}

const CARD_TONE: Record<GroupSummary["status"], string> = {
  // No ar é quieto: numa parede de cartões, o olho precisa cair nos
  // poucos que não estão bem.
  up: "border-line bg-surface",
  degraded: "border-degraded/50 bg-degraded-dim",
  down: "border-down/50 bg-down-dim",
  unknown: "border-line-strong bg-sunken",
  empty: "border-line bg-surface",
};

const WORD_TONE: Record<GroupSummary["status"], string> = {
  up: "text-ink-3",
  degraded: "text-degraded",
  down: "text-down",
  unknown: "text-ink-2",
  empty: "text-ink-3",
};

/**
 * GroupCard é o cartão de um ambiente: nome, cor do pior estado e uma
 * palavra. É um link de verdade, inteiro: abre em nova aba com Ctrl e se
 * alcança pelo teclado.
 */
function GroupCard({ card, system }: { card: GroupSummary; system: string }) {
  const palavra = cardWord(card);
  // Com o grupo todo pausado ou vazio, o pior estado é "sem medição",
  // mas pintar o cartão de problema seria alarmar por decisão de alguém.
  const tom = palavra === "pausado" || palavra === "vazio" ? "up" : card.status;
  const rota = { name: "group" as const, id: card.group_id };

  return (
    <a
      href={href(rota)}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
        e.preventDefault();
        navigate(rota);
      }}
      className={`pressable flex min-h-[64px] flex-col justify-center gap-0.5 rounded-md border px-3 py-2.5 hover:border-ink-3 active:bg-pressed sm:w-[168px] ${CARD_TONE[tom]}`}
    >
      <span className="flex items-center gap-2">
        <StatusDot status={card.status} />
        <span className="truncate text-lead font-medium text-ink">
          {system && <span className="sr-only">{system}, </span>}
          {card.name}
        </span>
      </span>
      <span className={`pl-[15px] text-small font-medium ${WORD_TONE[tom]}`}>{palavra}</span>
    </a>
  );
}

/**
 * ProblemsNow é o que exige ação, um por linha, do mais grave para o
 * menos. Cada linha leva à falha que a explica, já em destaque.
 */
function ProblemsNow({ items }: { items: Problem[] }) {
  return (
    <section aria-labelledby="problemas-agora" className="flex flex-col gap-2">
      <h2 id="problemas-agora" className="eyebrow flex items-baseline gap-2">
        <span className="text-down">problemas agora</span>
        <span className="tabular">{items.length}</span>
      </h2>
      <ul className="overflow-hidden rounded-md border border-line-strong bg-surface">
        {items.map((p) => (
          <li key={p.monitor_id} className="border-b border-line last:border-b-0">
            <ProblemRow problem={p} />
          </li>
        ))}
      </ul>
    </section>
  );
}

const PROBLEM_TONE: Record<Problem["status"], string> = {
  down: "text-down",
  degraded: "text-degraded",
  unknown: "text-ink-2",
};

function ProblemRow({ problem: p }: { problem: Problem }) {
  const causa = p.cause ? explainCause(p.cause) : null;
  // "worker default sem sinal" seguido de "sem sinal" repetiria a mesma
  // palavra; aí só o texto técnico acrescenta alguma coisa.
  const repete = causa?.summary === problemWord(p.status, p.type);

  return (
    <RowLink
      to={problemRoute(p)}
      className="grid grid-cols-[minmax(0,1fr)_auto] items-baseline gap-x-4 gap-y-0.5 px-3 py-2.5 sm:grid-cols-[180px_minmax(0,1fr)_auto]"
    >
      <span className="col-span-2 flex min-w-0 items-center gap-2 sm:col-span-1">
        <StatusDot status={p.status} />
        <span className="truncate text-small text-ink-2">{crumb(p.group_path)}</span>
      </span>

      <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 pl-[15px] sm:pl-0">
        <span className="truncate text-body font-medium text-ink">{p.name}</span>
        <span className={`text-body ${PROBLEM_TONE[p.status]}`}>{problemWord(p.status, p.type)}</span>
      </span>

      <span className="tabular flex items-baseline gap-3 whitespace-nowrap text-small text-ink-3">
        há {elapsed(p.since)}
        <span aria-hidden className="text-ink-2">
          →
        </span>
      </span>

      {causa && (
        <span className="col-span-2 flex min-w-0 flex-col pl-[15px] sm:col-start-2 sm:pl-0">
          {!repete && <span className="truncate text-small text-ink-2">{causa.summary}</span>}
          {/* O texto técnico fica discreto e cortado, inteiro no title: a
              linha é um link, e um botão de expandir dentro dele seria
              controle dentro de controle. O detalhe mostra inteiro. */}
          {causa.known && (
            <span className="tabular truncate text-micro text-ink-3" title={causa.raw}>
              {causa.raw}
            </span>
          )}
        </span>
      )}
    </RowLink>
  );
}

function SemAcesso() {
  return (
    <div className="mx-auto max-w-6xl px-5 py-6">
      <Empty
        title="Nenhum grupo no seu acesso"
        description="Esta conta ainda não enxerga nenhum grupo de monitores. Peça a um administrador para conceder o grupo que você acompanha."
      />
    </div>
  );
}
