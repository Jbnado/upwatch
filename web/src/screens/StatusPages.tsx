import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import {
  ApiError,
  type Announcement,
  type IncidentImpact,
  type IncidentPhase,
  type Monitor,
  type StatusPage,
  type StatusPageComponent,
  type StatusPageGroup,
} from "../api/types";
import {
  Alert,
  Button,
  Checkbox,
  Field,
  Input,
  Loading,
  Nothing,
  RowLink,
  Select,
  TextLink,
} from "../components/ui";
import { stamp } from "../lib/format";
import { navigate } from "../lib/router";

/**
 * Administração das páginas públicas.
 *
 * Duas telas: a lista, e a edição de uma página com seus grupos,
 * componentes e relatos. O que se escreve aqui é exatamente o que
 * qualquer pessoa com o link vê depois, então cada campo diz em voz alta
 * se é público.
 */

const IMPACTOS: { value: IncidentImpact; label: string }[] = [
  { value: "none", label: "Aviso, sem degradação" },
  { value: "minor", label: "Degradação parcial" },
  { value: "major", label: "Funcionalidade indisponível" },
  { value: "critical", label: "Serviço fora do ar" },
];

const FASES: { value: IncidentPhase; label: string }[] = [
  { value: "investigating", label: "Investigando" },
  { value: "identified", label: "Identificado" },
  { value: "monitoring", label: "Monitorando" },
  { value: "resolved", label: "Resolvido" },
];

// ---------- lista ----------

export function StatusPages() {
  const [paginas, setPaginas] = useState<StatusPage[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const { items } = await api.listStatusPages();
    setPaginas(items);
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-6 px-5 py-6">
      <div className="flex flex-col gap-6">
        <nav>
          <TextLink onClick={() => navigate({ name: "board" })}>← todos os monitores</TextLink>
        </nav>
        <div className="flex flex-col gap-1.5">
          <h1 className="text-title font-semibold tracking-tight">Páginas de estado</h1>
          <p className="text-body text-ink-2">
            Endereços públicos que qualquer pessoa abre sem entrar. Mostram apenas o que você
            publicar — nunca o endereço do alvo nem a causa detectada.
          </p>
        </div>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <NovaPagina onCreated={carregar} onError={setErro} />

      {paginas === null ? (
        <Loading what="páginas" />
      ) : paginas.length === 0 ? (
        <Nothing hint="Crie uma para compartilhar o estado dos seus serviços com clientes e com o time, sem dar acesso ao painel.">
          Nenhuma página publicada.
        </Nothing>
      ) : (
        <div className="border-t border-line">
          {paginas.map((p) => (
            <RowLink
              key={p.id}
              to={{ name: "status-page", id: p.id }}
              className="flex items-center justify-between gap-4 border-b border-line px-1 py-2.5"
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate text-body font-medium">{p.title}</span>
                <span className="tabular truncate text-small text-ink-3">/status/{p.slug}</span>
              </span>
              <span className="shrink-0 text-small text-ink-3">
                {p.default && <span className="mr-3 text-up">padrão</span>}
                {p.enabled ? "publicada" : "desligada"}
              </span>
            </RowLink>
          ))}
        </div>
      )}
    </div>
  );
}

function NovaPagina({
  onCreated,
  onError,
}: {
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [tocouNoSlug, setTocouNoSlug] = useState(false);
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);

  // O slug se escreve sozinho a partir do título, até alguém editá-lo.
  // Escrever "Estado da Plataforma" e ter que traduzir para
  // "estado-da-plataforma" à mão é trabalho que o navegador faz melhor.
  function mudarTitulo(valor: string) {
    setTitle(valor);
    if (!tocouNoSlug) setSlug(slugify(valor));
  }

  async function criar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setCampoErro(null);

    try {
      const criada = await api.createStatusPage({ slug, title });
      setTitle("");
      setSlug("");
      setTocouNoSlug(false);
      await onCreated();
      navigate({ name: "status-page", id: criada.id });
    } catch (e) {
      if (e instanceof ApiError && e.field) setCampoErro({ campo: e.field, msg: e.message });
      else if (e instanceof ApiError) onError(e.message);
      else onError("Não foi possível criar a página.");
    } finally {
      setEnviando(false);
    }
  }

  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);

  return (
    <form
      onSubmit={criar}
      className="flex flex-col gap-4 rounded-sm border border-line-strong p-4"
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="título" hint="Aparece no topo da página." error={erroDe("title")}>
          <Input
            value={title}
            onChange={(e) => mudarTitulo(e.target.value)}
            placeholder="Estado da plataforma"
            required
          />
        </Field>

        <Field
          label="endereço"
          hint={slug ? `/status/${slug}` : "Minúsculas, números e hífen."}
          error={erroDe("slug")}
        >
          <Input
            value={slug}
            onChange={(e) => {
              setTocouNoSlug(true);
              setSlug(e.target.value);
            }}
            placeholder="estado"
            className="tabular"
            required
          />
        </Field>
      </div>

      <Button type="submit" variant="primary" disabled={enviando} className="self-start">
        {enviando ? "Criando" : "Criar página"}
      </Button>
    </form>
  );
}

/**
 * slugify traduz um título em endereço.
 *
 * Remove acento pela decomposição Unicode, que o navegador já sabe fazer
 * — no servidor isso custaria uma dependência inteira para o mesmo
 * resultado.
 */
export function slugify(texto: string): string {
  return texto
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64);
}

// ---------- edição ----------

export function StatusPageAdmin({ id }: { id: number }) {
  const [page, setPage] = useState<StatusPage | null>(null);
  const [groups, setGroups] = useState<StatusPageGroup[]>([]);
  const [components, setComponents] = useState<StatusPageComponent[]>([]);
  const [monitors, setMonitors] = useState<Monitor[]>([]);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const [detalhe, lista] = await Promise.all([
        api.getStatusPage(id),
        api.listMonitors({ limit: 200 }),
      ]);
      setPage(detalhe.page);
      setGroups(detalhe.groups);
      setComponents(detalhe.components);
      setMonitors(lista.items);
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar a página.");
    }
  }, [id]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  if (erro) {
    return (
      <div className="mx-auto max-w-2xl px-5 py-6">
        <Alert>{erro}</Alert>
      </div>
    );
  }
  if (!page) {
    return (
      <div className="mx-auto max-w-2xl px-5 py-6">
        <Loading what="a página" />
      </div>
    );
  }

  async function remover() {
    if (!confirm(`Remover "${page!.title}"? O endereço deixa de responder.`)) return;

    await api.deleteStatusPage(page!.id);
    navigate({ name: "status-pages" });
  }

  const publicados = new Set(components.map((c) => c.monitor_id));

  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-10 px-5 py-6">
      <div className="flex flex-col gap-6">
        <nav>
          <TextLink onClick={() => navigate({ name: "status-pages" })}>
            ← páginas de estado
          </TextLink>
        </nav>

        <header className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex flex-col gap-1.5">
            <h1 className="text-title font-semibold tracking-tight">{page.title}</h1>
            {/* O endereço público, clicável: conferir como a página ficou
                é a primeira coisa que se quer fazer depois de mexer nela. */}
            <a
              href={page.default ? "/status" : `/status/${page.slug}`}
              target="_blank"
              rel="noreferrer"
              className="pressable tabular text-small text-ink-3 underline decoration-line-strong hover:decoration-ink"
            >
              {page.default ? "/status" : `/status/${page.slug}`} ↗
            </a>
          </div>
          <div className="flex items-center gap-2">
            {!page.default && (
              <Button
                onClick={async () => {
                  await api.setDefaultStatusPage(page.id);
                  await carregar();
                }}
              >
                Tornar padrão
              </Button>
            )}
            <Button variant="danger" onClick={remover}>
              Remover
            </Button>
          </div>
        </header>

        {page.default ? (
          <p className="text-small text-ink-3">
            Esta é a página padrão: responde em <span className="tabular">/status</span>, sem o
            slug no caminho.
          </p>
        ) : (
          <p className="text-small text-ink-3">
            Torne-a padrão para responder em <span className="tabular">/status</span> — numa
            instalação com uma página só, o slug repete o que o caminho já diz.
          </p>
        )}
      </div>

      <Ajustes page={page} onSaved={carregar} />

      <section className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <h2 className="text-lead font-medium">Serviços publicados</h2>
          <p className="text-body text-ink-2">
            Marque o que aparece na página. O rótulo é o nome que o público lê — o monitor mantém o
            nome interno.
          </p>
        </div>

        {monitors.length === 0 ? (
          <Nothing>Nenhum monitor cadastrado ainda.</Nothing>
        ) : (
          <ul className="-mx-1 flex flex-col">
            {monitors.map((m) => (
              <li key={m.id}>
                <ComponenteLinha
                  monitor={m}
                  pageID={page.id}
                  groups={groups}
                  component={components.find((c) => c.monitor_id === m.id)}
                  publicado={publicados.has(m.id)}
                  onChanged={carregar}
                />
              </li>
            ))}
          </ul>
        )}
      </section>

      <Grupos pageID={page.id} groups={groups} onChanged={carregar} />

      <Relatos monitors={monitors} onChanged={carregar} />
    </div>
  );
}

function Ajustes({ page, onSaved }: { page: StatusPage; onSaved: () => Promise<void> }) {
  const [title, setTitle] = useState(page.title);
  const [description, setDescription] = useState(page.description ?? "");
  const [timeZone, setTimeZone] = useState(page.time_zone ?? "");
  const [showLatency, setShowLatency] = useState(page.show_latency);
  const [enabled, setEnabled] = useState(page.enabled);
  const [erro, setErro] = useState<string | null>(null);
  const [salvo, setSalvo] = useState(false);

  async function salvar(e: FormEvent) {
    e.preventDefault();
    setErro(null);
    setSalvo(false);

    try {
      await api.updateStatusPage(page.id, {
        slug: page.slug,
        title,
        description,
        time_zone: timeZone,
        show_latency: showLatency,
        enabled,
      });
      await onSaved();
      setSalvo(true);
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível salvar.");
    }
  }

  return (
    <form onSubmit={salvar} className="flex flex-col gap-4">
      <h2 className="text-lead font-medium">Ajustes da página</h2>

      {erro && <Alert>{erro}</Alert>}

      <Field label="título">
        <Input value={title} onChange={(e) => setTitle(e.target.value)} required />
      </Field>

      <Field label="descrição" hint="Uma linha sob o título, opcional.">
        <Input value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field>

      <Field
        label="fuso horário"
        hint="Em que horário a página fala. Vazio usa UTC."
      >
        <Input
          value={timeZone}
          onChange={(e) => setTimeZone(e.target.value)}
          placeholder="America/Sao_Paulo"
          className="tabular"
        />
      </Field>

      <div className="-mx-1 flex flex-col">
        <Checkbox checked={showLatency} onChange={() => setShowLatency((v) => !v)}>
          <span>Mostrar latência</span>
          <span className="text-small text-ink-3">
            desligado, a página só diz se está no ar
          </span>
        </Checkbox>
        <Checkbox checked={enabled} onChange={() => setEnabled((v) => !v)}>
          <span>Publicada</span>
          <span className="text-small text-ink-3">desligada, o endereço devolve 404</span>
        </Checkbox>
      </div>

      <div className="flex items-center gap-3">
        <Button type="submit" variant="primary" className="self-start">
          Salvar
        </Button>
        {salvo && <span className="text-small text-up">salvo</span>}
      </div>
    </form>
  );
}

function ComponenteLinha({
  monitor,
  pageID,
  groups,
  component,
  publicado,
  onChanged,
}: {
  monitor: Monitor;
  pageID: number;
  groups: StatusPageGroup[];
  component?: StatusPageComponent;
  publicado: boolean;
  onChanged: () => Promise<void>;
}) {
  const [label, setLabel] = useState(component?.label ?? "");

  async function alternar() {
    if (publicado) await api.removeComponent(pageID, monitor.id);
    else await api.setComponent(pageID, monitor.id, { label });
    await onChanged();
  }

  async function salvarRotulo() {
    if (!publicado || label === (component?.label ?? "")) return;
    await api.setComponent(pageID, monitor.id, { label, group_id: component?.group_id ?? null });
    await onChanged();
  }

  async function mudarGrupo(valor: string) {
    await api.setComponent(pageID, monitor.id, {
      label,
      group_id: valor === "" ? null : Number(valor),
    });
    await onChanged();
  }

  return (
    <div className="flex flex-col gap-2 py-1">
      <Checkbox checked={publicado} onChange={alternar}>
        <span>{monitor.name}</span>
        {!publicado && <span className="text-small text-ink-3">não publicado</span>}
      </Checkbox>

      {publicado && (
        <div className="ml-6 flex flex-wrap items-end gap-3">
          <div className="min-w-[180px] flex-1">
            <Field label="rótulo público" hint={label ? undefined : `usa "${monitor.name}"`}>
              <Input
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                onBlur={salvarRotulo}
                placeholder={monitor.name}
              />
            </Field>
          </div>
          {groups.length > 0 && (
            <div className="min-w-[160px]">
              <Field label="grupo">
                <Select
                  value={component?.group_id ? String(component.group_id) : ""}
                  onChange={(e) => mudarGrupo(e.target.value)}
                >
                  <option value="">Sem grupo</option>
                  {groups.map((g) => (
                    <option key={g.id} value={g.id}>
                      {g.name}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function Grupos({
  pageID,
  groups,
  onChanged,
}: {
  pageID: number;
  groups: StatusPageGroup[];
  onChanged: () => Promise<void>;
}) {
  const [nome, setNome] = useState("");

  async function criar(e: FormEvent) {
    e.preventDefault();
    if (!nome.trim()) return;

    await api.createGroup(pageID, { name: nome, position: groups.length + 1 });
    setNome("");
    await onChanged();
  }

  async function remover(g: StatusPageGroup) {
    // Vale dizer o que não acontece: quem apaga um grupo costuma temer
    // que os serviços saiam da página junto.
    if (!confirm(`Remover o grupo "${g.name}"? Os serviços continuam publicados, sem grupo.`)) {
      return;
    }
    await api.deleteGroup(pageID, g.id);
    await onChanged();
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Grupos</h2>
        <p className="text-body text-ink-2">
          Seções da página, como "API" e "Console". Sem grupo, quarenta serviços viram uma lista
          onde ninguém acha o que veio procurar.
        </p>
      </div>

      {groups.length === 0 ? (
        <Nothing>Nenhum grupo. Os serviços aparecem numa lista única.</Nothing>
      ) : (
        <ul className="border-t border-line">
          {groups.map((g) => (
            <li
              key={g.id}
              className="flex items-center justify-between gap-4 border-b border-line py-2"
            >
              <span className="text-body">{g.name}</span>
              <Button size="sm" variant="danger" onClick={() => remover(g)}>
                Remover
              </Button>
            </li>
          ))}
        </ul>
      )}

      <form onSubmit={criar} className="flex gap-2">
        <Input
          value={nome}
          onChange={(e) => setNome(e.target.value)}
          placeholder="API"
          className="flex-1"
        />
        <Button type="submit" className="shrink-0">
          Criar grupo
        </Button>
      </form>
    </section>
  );
}

// ---------- relatos ----------

function Relatos({
  monitors,
  onChanged,
}: {
  monitors: Monitor[];
  onChanged: () => Promise<void>;
}) {
  const [items, setItems] = useState<Announcement[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    const page = await api.announcements({ limit: 20 });
    setItems(page.items);
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Relatos</h2>
        <p className="text-body text-ink-2">
          O texto que aparece em "incidentes anteriores". A causa detectada pela sonda nunca é
          publicada — ela cita host e porta internos —, então o que o público lê é o que você
          escrever aqui.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <NovoRelato
        monitors={monitors}
        onCreated={async () => {
          await carregar();
          await onChanged();
        }}
        onError={setErro}
      />

      {items === null ? (
        <Loading what="relatos" />
      ) : items.length === 0 ? (
        <Nothing hint="Uma instalação sem relatos mostra as barras e “nenhum incidente relatado”, que é o estado saudável.">
          Nenhum relato publicado.
        </Nothing>
      ) : (
        <ul className="border-t border-line">
          {items.map((a) => (
            <RelatoLinha key={a.id} a={a} onChanged={carregar} />
          ))}
        </ul>
      )}
    </section>
  );
}

function RelatoLinha({ a, onChanged }: { a: Announcement; onChanged: () => Promise<void> }) {
  const [aberto, setAberto] = useState(false);
  const [fase, setFase] = useState<IncidentPhase>(a.phase);
  const [corpo, setCorpo] = useState("");

  async function publicar(e: FormEvent) {
    e.preventDefault();
    if (!corpo.trim()) return;

    await api.publishUpdate(a.id, { phase: fase, body: corpo });
    setCorpo("");
    setAberto(false);
    await onChanged();
  }

  async function remover() {
    if (!confirm(`Remover "${a.title}"? Ele some da página pública.`)) return;

    await api.deleteAnnouncement(a.id);
    await onChanged();
  }

  return (
    <li className="flex flex-col gap-2 border-b border-line py-2.5">
      <div className="flex items-center justify-between gap-4">
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-body font-medium">{a.title}</span>
          <span className="text-small text-ink-3">
            {FASES.find((f) => f.value === a.phase)?.label ?? a.phase}
            <span className="mx-1.5">·</span>
            <span className="tabular">{stamp(a.started_at)}</span>
          </span>
        </span>

        <span className="flex shrink-0 items-center gap-2">
          <Button size="sm" onClick={() => setAberto((v) => !v)}>
            {aberto ? "Fechar" : "Atualizar"}
          </Button>
          <Button size="sm" variant="danger" onClick={remover}>
            Remover
          </Button>
        </span>
      </div>

      {aberto && (
        <form onSubmit={publicar} className="flex flex-col gap-3 pt-1">
          <div className="grid gap-3 sm:grid-cols-[180px_minmax(0,1fr)]">
            <Field label="fase">
              <Select value={fase} onChange={(e) => setFase(e.target.value as IncidentPhase)}>
                {FASES.map((f) => (
                  <option key={f.value} value={f.value}>
                    {f.label}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="o que contar" hint="Aparece na linha do tempo pública.">
              <Input
                value={corpo}
                onChange={(e) => setCorpo(e.target.value)}
                placeholder="Identificamos a causa e aplicamos a correção."
                required
              />
            </Field>
          </div>
          <Button type="submit" variant="primary" className="self-start">
            Publicar atualização
          </Button>
        </form>
      )}
    </li>
  );
}

function NovoRelato({
  monitors,
  onCreated,
  onError,
}: {
  monitors: Monitor[];
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [impact, setImpact] = useState<IncidentImpact>("minor");
  const [body, setBody] = useState("");
  const [global, setGlobal] = useState(false);
  const [afetados, setAfetados] = useState<Set<number>>(new Set());
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);
  const [enviando, setEnviando] = useState(false);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setCampoErro(null);

    try {
      await api.createAnnouncement({
        title,
        impact,
        phase: "investigating",
        global,
        components: [...afetados],
        body: body || undefined,
      });
      setTitle("");
      setBody("");
      setAfetados(new Set());
      setGlobal(false);
      await onCreated();
    } catch (e) {
      if (e instanceof ApiError && e.field) setCampoErro({ campo: e.field, msg: e.message });
      else if (e instanceof ApiError) onError(e.message);
      else onError("Não foi possível publicar o relato.");
    } finally {
      setEnviando(false);
    }
  }

  function alternar(id: number) {
    setAfetados((atual) => {
      const proximo = new Set(atual);
      if (proximo.has(id)) proximo.delete(id);
      else proximo.add(id);
      return proximo;
    });
  }

  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);

  return (
    <form onSubmit={criar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_220px]">
        <Field label="título" hint="A manchete que o público lê." error={erroDe("title")}>
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Lentidão na API de pagamentos"
            required
          />
        </Field>
        <Field label="impacto">
          <Select value={impact} onChange={(e) => setImpact(e.target.value as IncidentImpact)}>
            {IMPACTOS.map((i) => (
              <option key={i.value} value={i.value}>
                {i.label}
              </option>
            ))}
          </Select>
        </Field>
      </div>

      <Field label="primeira atualização" hint="Opcional, mas é o que a página mostra primeiro.">
        <Input
          value={body}
          onChange={(e) => setBody(e.target.value)}
          placeholder="Estamos investigando erros elevados na API."
        />
      </Field>

      <div className="flex flex-col gap-1.5">
        <span className="eyebrow">o que foi afetado</span>
        {erroDe("components") && (
          <span className="text-small text-down">{erroDe("components")}</span>
        )}

        <div className="-mx-1 flex flex-col">
          <Checkbox checked={global} onChange={() => setGlobal((v) => !v)}>
            <span>Toda a plataforma</span>
            <span className="text-small text-ink-3">aparece em todas as páginas</span>
          </Checkbox>

          {!global &&
            monitors.map((m) => (
              <Checkbox key={m.id} checked={afetados.has(m.id)} onChange={() => alternar(m.id)}>
                <span>{m.name}</span>
              </Checkbox>
            ))}
        </div>
      </div>

      <Button type="submit" variant="primary" disabled={enviando} className="self-start">
        {enviando ? "Publicando" : "Publicar relato"}
      </Button>
    </form>
  );
}
