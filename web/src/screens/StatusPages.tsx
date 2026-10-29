import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api } from "../api/client";
import {
  ApiError,
  type Announcement,
  type Group,
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
  FormActions,
  Input,
  Loading,
  NewItem,
  Nothing,
  RowLink,
  Select,
  TextLink,
} from "../components/ui";
import { MonitorTree } from "../components/MonitorTree";
import { stamp } from "../lib/format";
import { navigate } from "../lib/router";
import { serviceOrigin } from "../lib/service-tree";

/**
 * Administração das páginas públicas.
 *
 * Duas telas: a lista, e a edição de uma página com suas seções,
 * serviços e relatos. O que se escreve aqui é exatamente o que
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
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Páginas de estado</h2>
        <p className="text-body text-ink-2">
          Endereços públicos que qualquer pessoa abre sem entrar, para compartilhar disponibilidade
          com clientes e com o time. Mostram apenas o que você publicar — nunca o endereço do alvo
          nem a causa detectada.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

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
              to={{ name: "ajustes-pagina", id: p.id }}
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

      <NewItem label="Nova página">
        {(fechar) => <NovaPagina onCreated={carregar} onError={setErro} onCancel={fechar} />}
      </NewItem>
    </section>
  );
}

function NovaPagina({
  onCreated,
  onError,
  onCancel,
}: {
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
  onCancel: () => void;
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
      await onCreated();
      navigate({ name: "ajustes-pagina", id: criada.id });
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
            autoFocus
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

      <FormActions submit={enviando ? "Criando" : "Criar página"} busy={enviando} onCancel={onCancel} />
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
  const [secoes, setSecoes] = useState<StatusPageGroup[]>([]);
  const [components, setComponents] = useState<StatusPageComponent[]>([]);
  const [monitors, setMonitors] = useState<Monitor[]>([]);
  const [grupos, setGrupos] = useState<Group[]>([]);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const [detalhe, lista, g] = await Promise.all([
        api.getStatusPage(id),
        api.listMonitors({ limit: 200 }),
        api.listGroups(),
      ]);
      setPage(detalhe.page);
      setSecoes(detalhe.groups);
      setComponents(detalhe.components);
      setMonitors(lista.items);
      setGrupos(g.items);
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : "Não foi possível carregar a página.");
    }
  }, [id]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const voltar = (
    <nav aria-label="Voltar às páginas">
      <TextLink onClick={() => navigate({ name: "ajustes", section: "paginas" })}>
        ← páginas de estado
      </TextLink>
    </nav>
  );

  if (erro && !page) {
    return (
      <div className="flex flex-col gap-6">
        {voltar}
        <Alert>{erro}</Alert>
      </div>
    );
  }
  if (!page) return <Loading what="a página" />;

  async function remover() {
    if (!confirm(`Remover "${page!.title}"? O endereço deixa de responder.`)) return;

    await api.deleteStatusPage(page!.id);
    navigate({ name: "ajustes", section: "paginas" });
  }

  const publico = page.default ? "/status" : `/status/${page.slug}`;

  return (
    <div className="flex flex-col gap-10">
      <div className="flex flex-col gap-6">
        {voltar}

        <header className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h2 className="text-title font-semibold tracking-tight">{page.title}</h2>
            {/* O endereço público, clicável: conferir como a página ficou
                é a primeira coisa que se quer fazer depois de mexer nela. */}
            <a
              href={publico}
              target="_blank"
              rel="noreferrer"
              className="pressable text-action tabular self-start text-small text-ink-3 underline decoration-line-strong hover:decoration-ink"
            >
              {publico} ↗
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
        {!page.enabled && (
          <p className="border-l-2 border-degraded bg-degraded-dim px-3 py-2 text-body">
            Desligada: o endereço devolve 404 até você marcar "Publicada" nos ajustes da página,
            no fim desta tela.
          </p>
        )}
      </div>

      <Secoes pageID={page.id} secoes={secoes} onChanged={carregar} />

      <Servicos
        pageID={page.id}
        secoes={secoes}
        components={components}
        setComponents={setComponents}
        monitors={monitors}
        grupos={grupos}
        onChanged={carregar}
      />

      <Relatos monitors={monitors} grupos={grupos} onChanged={carregar} />

      <Ajustes page={page} onSaved={carregar} />
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
      <h3 className="text-lead font-medium">Ajustes da página</h3>

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

/**
 * Servicos é o que a página mostra: primeiro como ela fica, seção por
 * seção, e depois a árvore para escolher.
 *
 * Os nomes repetem entre ambientes — há um "erp" em produção e outro em
 * homologação —, então tudo aqui diz de onde cada serviço vem. Publicar o
 * de homologação no lugar do de produção mostraria ao cliente um estado
 * que não é o dele.
 */
function Servicos({
  pageID,
  secoes,
  components,
  setComponents,
  monitors,
  grupos,
  onChanged,
}: {
  pageID: number;
  secoes: StatusPageGroup[];
  components: StatusPageComponent[];
  setComponents: (c: StatusPageComponent[]) => void;
  monitors: Monitor[];
  grupos: Group[];
  onChanged: () => Promise<void>;
}) {
  const [erro, setErro] = useState<string | null>(null);
  const publicados = new Set(components.map((c) => c.monitor_id));

  async function escolher(proximo: Set<number>) {
    const entram = [...proximo].filter((id) => !publicados.has(id));
    const saem = [...publicados].filter((id) => !proximo.has(id));
    setErro(null);

    // A caixa responde na hora e o servidor confirma em seguida: esperar
    // a volta de seis requisições para ver a marca aparecer pareceria um
    // clique que não pegou.
    setComponents([
      ...components.filter((c) => !saem.includes(c.monitor_id)),
      ...entram.map((id) => ({ page_id: pageID, monitor_id: id, group_id: null, position: 0 })),
    ]);

    try {
      await Promise.all([
        ...entram.map((id) => api.setComponent(pageID, id, { label: "" })),
        ...saem.map((id) => api.removeComponent(pageID, id)),
      ]);
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível mudar o que está publicado.");
    }
    await onChanged();
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-lead font-medium">Serviços publicados</h3>
        <p className="text-body text-ink-2">
          O rótulo é o nome que o público lê. O monitor mantém o nome interno, e o grupo de onde
          ele vem não aparece na página.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <NaPagina components={components} secoes={secoes} monitors={monitors} grupos={grupos} />

      <div className="flex flex-col gap-2">
        <span className="eyebrow">escolher serviços</span>
        <MonitorTree
          groups={grupos}
          monitors={monitors}
          selected={publicados}
          onChange={escolher}
          renderDetail={(monitorId) => {
            const c = components.find((x) => x.monitor_id === monitorId);
            const m = monitors.find((x) => x.id === monitorId);
            if (!c || !m) return null;
            return (
              <ComponenteDetalhe
                key={monitorId}
                pageID={pageID}
                monitor={m}
                component={c}
                secoes={secoes}
                onChanged={onChanged}
              />
            );
          }}
        />
        <p className="max-w-prose text-small text-ink-3">
          Marcar um ambiente ou um sistema publica os serviços que ele tem agora. Serviço criado
          depois não entra sozinho na página pública.
        </p>
      </div>
    </section>
  );
}

/**
 * NaPagina é a página como o público vai lê-la, seção por seção, com o
 * grupo de origem de cada serviço ao lado — só aqui, nunca lá.
 */
function NaPagina({
  components,
  secoes,
  monitors,
  grupos,
}: {
  components: StatusPageComponent[];
  secoes: StatusPageGroup[];
  monitors: Monitor[];
  grupos: Group[];
}) {
  if (components.length === 0) {
    return (
      <Nothing hint="Marque abaixo o que vai aparecer. Sem serviço, a página pública mostra só o título.">
        Nenhum serviço publicado.
      </Nothing>
    );
  }

  const blocos = [
    ...[...secoes]
      .sort((a, b) => a.position - b.position)
      .map((s) => ({
        key: `s${s.id}`,
        titulo: s.name,
        itens: components.filter((c) => c.group_id === s.id),
      })),
    {
      key: "sem-secao",
      titulo: secoes.length > 0 ? "Sem seção" : "",
      itens: components.filter((c) => !c.group_id || !secoes.some((s) => s.id === c.group_id)),
    },
  ].filter((b) => b.itens.length > 0);

  return (
    <div className="flex flex-col gap-4 rounded-sm border border-line p-4">
      <span className="eyebrow">na página</span>
      {blocos.map((b) => (
        <div key={b.key} className="flex flex-col gap-1">
          {b.titulo && <span className="text-body font-medium">{b.titulo}</span>}
          <ul className="flex flex-col border-t border-line">
            {[...b.itens]
              .sort((x, y) => x.position - y.position)
              .map((c) => {
                const m = monitors.find((x) => x.id === c.monitor_id);
                const nome = m?.name ?? `#${c.monitor_id}`;
                return (
                  <li
                    key={c.monitor_id}
                    className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 border-b border-line py-1.5"
                  >
                    <span className="min-w-0 truncate text-body">{c.label || nome}</span>
                    <span className="min-w-0 truncate text-small text-ink-3">
                      {nome}
                      <span className="mx-1.5">·</span>
                      {m ? serviceOrigin(m, grupos) : "fora do seu acesso"}
                    </span>
                  </li>
                );
              })}
          </ul>
        </div>
      ))}
    </div>
  );
}

/**
 * ComponenteDetalhe é o rótulo público e a seção de um serviço marcado,
 * logo abaixo dele na árvore.
 */
function ComponenteDetalhe({
  pageID,
  monitor,
  component,
  secoes,
  onChanged,
}: {
  pageID: number;
  monitor: Monitor;
  component: StatusPageComponent;
  secoes: StatusPageGroup[];
  onChanged: () => Promise<void>;
}) {
  const [label, setLabel] = useState(component.label ?? "");
  const [secao, setSecao] = useState<number | null>(component.group_id ?? null);
  // O último par enviado, e a fila das gravações. Rótulo e seção vão
  // sempre juntos e um de cada vez: digitar o rótulo e ir direto à seção
  // dispara duas gravações, e em paralelo o servidor ficava com a que
  // chegasse por último — às vezes a do rótulo, ainda com a seção antiga.
  const enviado = useRef({ label: component.label ?? "", secao: component.group_id ?? null });
  const fila = useRef<Promise<unknown>>(Promise.resolve());

  function salvar(proximo: { label: string; secao: number | null }) {
    if (proximo.label === enviado.current.label && proximo.secao === enviado.current.secao) return;
    enviado.current = proximo;
    fila.current = fila.current
      .then(() =>
        api.setComponent(pageID, monitor.id, { label: proximo.label, group_id: proximo.secao }),
      )
      .then(onChanged, onChanged);
  }

  const salvarRotulo = () => salvar({ label, secao });

  function mudarSecao(valor: string) {
    const proxima = valor === "" ? null : Number(valor);
    setSecao(proxima);
    salvar({ label, secao: proxima });
  }

  return (
    <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,11rem)]">
      <Field label="rótulo público" hint={label ? undefined : `vazio, usa "${monitor.name}"`}>
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          onBlur={salvarRotulo}
          onKeyDown={(e) => {
            // Enter salva em vez de não fazer nada: o campo está fora de
            // formulário, e a pessoa espera que o Enter confirme.
            if (e.key === "Enter") {
              e.preventDefault();
              salvarRotulo();
            }
          }}
          placeholder={monitor.name}
        />
      </Field>
      {secoes.length > 0 && (
        <Field label="seção">
          <Select value={secao ? String(secao) : ""} onChange={(e) => mudarSecao(e.target.value)}>
            <option value="">Sem seção</option>
            {[...secoes]
              .sort((a, b) => a.position - b.position)
              .map((g) => (
                <option key={g.id} value={g.id}>
                  {g.name}
                </option>
              ))}
          </Select>
        </Field>
      )}
    </div>
  );
}

/**
 * Secoes são as divisões da página pública, como "API" e "Console".
 *
 * Na API elas ainda se chamam grupos; na tela, não. "Grupo" já é o
 * Sistema › Ambiente dos monitores, e duas coisas com o mesmo nome na
 * mesma tela faziam a pessoa marcar uma achando que era a outra.
 */
function Secoes({
  pageID,
  secoes,
  onChanged,
}: {
  pageID: number;
  secoes: StatusPageGroup[];
  onChanged: () => Promise<void>;
}) {
  const [nome, setNome] = useState("");

  async function criar(e: FormEvent): Promise<boolean> {
    e.preventDefault();
    if (!nome.trim()) return false;

    await api.createGroup(pageID, { name: nome, position: secoes.length + 1 });
    setNome("");
    await onChanged();
    return true;
  }

  async function remover(g: StatusPageGroup) {
    // Vale dizer o que não acontece: quem apaga uma seção costuma temer
    // que os serviços saiam da página junto.
    if (!confirm(`Remover a seção "${g.name}"? Os serviços continuam publicados, sem seção.`)) {
      return;
    }
    await api.deleteGroup(pageID, g.id);
    await onChanged();
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-lead font-medium">Seções</h3>
        <p className="text-body text-ink-2">
          Divisões da página pública, como "API" e "Console". Sem seção, quarenta serviços viram
          uma lista onde ninguém acha o que veio procurar.
        </p>
      </div>

      {secoes.length === 0 ? (
        <Nothing>Nenhuma seção. Os serviços aparecem numa lista única.</Nothing>
      ) : (
        <ul className="border-t border-line">
          {[...secoes]
            .sort((a, b) => a.position - b.position)
            .map((g) => (
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

      <NewItem label="Nova seção">
        {(fechar) => (
          <form
            onSubmit={async (e) => {
              if (await criar(e)) fechar();
            }}
            className="flex flex-col gap-4 rounded-sm border border-line-strong p-4"
          >
            <Field label="nome da seção" hint="Aparece como título na página pública.">
              <Input
                value={nome}
                onChange={(e) => setNome(e.target.value)}
                placeholder="API"
                autoFocus
                required
              />
            </Field>
            <FormActions submit="Criar seção" onCancel={fechar} />
          </form>
        )}
      </NewItem>
    </section>
  );
}

// ---------- relatos ----------

function Relatos({
  monitors,
  grupos,
  onChanged,
}: {
  monitors: Monitor[];
  grupos: Group[];
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
        <h3 className="text-lead font-medium">Relatos</h3>
        <p className="text-body text-ink-2">
          O texto que aparece em "incidentes anteriores". A causa detectada pela sonda nunca é
          publicada — ela cita host e porta internos —, então o que o público lê é o que você
          escrever aqui.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

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

      <NewItem label="Novo relato">
        {(fechar) => (
          <NovoRelato
            monitors={monitors}
            grupos={grupos}
            onCreated={async () => {
              fechar();
              await carregar();
              await onChanged();
            }}
            onError={setErro}
            onCancel={fechar}
          />
        )}
      </NewItem>
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
  grupos,
  onCreated,
  onError,
  onCancel,
}: {
  monitors: Monitor[];
  grupos: Group[];
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
  onCancel: () => void;
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

  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);

  return (
    <form onSubmit={criar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_220px]">
        <Field label="título" hint="A manchete que o público lê." error={erroDe("title")}>
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Lentidão na API de pagamentos"
            autoFocus
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

      {/* fieldset e não Field: são várias caixas, e um label em volta de
          todas faria um clique no texto marcar a primeira. */}
      <fieldset className="flex flex-col gap-1.5">
        <legend className="eyebrow mb-1.5">o que foi afetado</legend>
        {erroDe("components") && (
          <span className="text-small text-down">{erroDe("components")}</span>
        )}

        <div className="-mx-1 flex flex-col">
          <Checkbox checked={global} onChange={() => setGlobal((v) => !v)}>
            <span>Toda a plataforma</span>
            <span className="text-small text-ink-3">aparece em todas as páginas</span>
          </Checkbox>
        </div>

        {!global && (
          <MonitorTree
            groups={grupos}
            monitors={monitors}
            selected={afetados}
            onChange={setAfetados}
          />
        )}
      </fieldset>

      <FormActions
        submit={enviando ? "Publicando" : "Publicar relato"}
        busy={enviando}
        onCancel={onCancel}
      />
    </form>
  );
}
