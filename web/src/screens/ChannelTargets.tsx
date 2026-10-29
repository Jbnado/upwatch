import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api } from "../api/client";
import type { ChannelTargets as Vinculos, Group, Monitor } from "../api/types";
import { CheckTree, type TreeItem } from "../components/CheckTree";
import { Input, Loading } from "../components/ui";
import {
  alternarAlvo,
  chavesDoNo,
  deVinculos,
  estadoDoAlvo,
  foraDaArvore,
  juntarVinculos,
  paraVinculos,
} from "../lib/channel-targets";
import { buildServiceTree, filterServiceTree, isCollapsible, type ServiceNode } from "../lib/service-tree";

/**
 * ChannelTargets escolhe, no formulário do canal, sobre o que ele avisa.
 *
 * Sem isto, criar um canal era o primeiro passo de três: depois era
 * preciso ir a cada grupo ou monitor e ligá-lo lá, e o canal recém-criado
 * não avisava ninguém até alguém lembrar.
 *
 * A árvore é a mesma da escolha de monitores, com os grupos vazios
 * também: ligar o canal ao ambiente antes do primeiro monitor faz esse
 * monitor já nascer avisando. Só avisa quem chama (onChange) depois de
 * um clique: sem mexer aqui, a edição não manda vínculos e o servidor
 * mantém os que já existem.
 */
export function ChannelTargets({
  inicial,
  onChange,
}: {
  inicial?: Vinculos;
  onChange: (vinculos: Vinculos) => void;
}) {
  const [dados, setDados] = useState<{ grupos: Group[]; monitores: Monitor[] } | null>(null);
  const [erro, setErro] = useState(false);

  useEffect(() => {
    let vivo = true;
    Promise.all([api.listGroups(), api.listMonitors({ limit: 500 })])
      .then(([g, m]) => vivo && setDados({ grupos: g.items, monitores: m.items }))
      .catch(() => vivo && setErro(true));
    return () => {
      vivo = false;
    };
  }, []);

  return (
    <div className="flex flex-col gap-2 border-t border-line pt-4">
      <span className="eyebrow">avisa sobre</span>
      {erro ? (
        <p className="text-small text-ink-3">
          Não foi possível carregar grupos e monitores. Salvar agora mantém os vínculos atuais.
        </p>
      ) : dados === null ? (
        <Loading what="grupos e monitores" />
      ) : (
        <Arvore grupos={dados.grupos} monitores={dados.monitores} inicial={inicial} onChange={onChange} />
      )}
    </div>
  );
}

function Arvore({
  grupos,
  monitores,
  inicial,
  onChange,
}: {
  grupos: Group[];
  monitores: Monitor[];
  inicial?: Vinculos;
  onChange: (vinculos: Vinculos) => void;
}) {
  const arvore = useMemo(
    () => buildServiceTree(grupos, monitores, { emptyGroups: true }),
    [grupos, monitores],
  );
  const [sel, setSel] = useState(() => deVinculos(arvore, inicial));
  const [busca, setBusca] = useState("");
  const [abertos, setAbertos] = useState(() => abertosComMarcado(arvore, sel));
  const invisiveis = useMemo(() => foraDaArvore(arvore, inicial), [arvore, inicial]);

  const buscando = busca.trim() !== "";
  const visivel = useMemo(() => filterServiceTree(arvore, busca), [arvore, busca]);
  const gravado = paraVinculos(arvore, sel);
  const ligadosPeloGrupo = new Set(gravado.group_ids.map((id) => `g${id}`));

  const porChave = useMemo(() => {
    const mapa = new Map<string, ServiceNode>();
    const visitar = (n: ServiceNode) => {
      mapa.set(n.key, n);
      n.children.forEach(visitar);
    };
    arvore.forEach(visitar);
    return mapa;
  }, [arvore]);

  function alternar(key: string) {
    const no = porChave.get(key);
    if (!no) return;
    // Na busca, o grupo marca só o que a busca deixou à vista, como na
    // escolha de monitores.
    const visto = buscando ? (acharVisivel(visivel, key) ?? no) : no;
    const proximo = alternarAlvo(visto, sel);
    setSel(proximo);
    onChange(juntarVinculos(paraVinculos(arvore, proximo), invisiveis));
  }

  const item = (n: ServiceNode, pai: ServiceNode | null): TreeItem => {
    const filhos = n.children.map((c) => item(c, n));
    if (n.monitorId !== undefined) {
      return {
        key: n.key,
        label: n.name,
        context: n.path,
        state: estadoDoAlvo(n, sel),
        collapsible: isCollapsible(n),
        children: filhos,
      };
    }
    return {
      key: n.key,
      label: n.name,
      context: pai ? pai.name : undefined,
      meta: metaDoGrupo(n, sel, ligadosPeloGrupo.has(n.key)),
      state: estadoDoAlvo(n, sel),
      strong: pai === null,
      collapsible: isCollapsible(n),
      children: filhos,
    };
  };

  if (arvore.length === 0) {
    return <p className="text-small text-ink-3">Nenhum grupo ou monitor cadastrado ainda.</p>;
  }

  const itens = visivel.map((n) => item(n, null));

  return (
    <div className="flex flex-col gap-2">
      <Input
        type="search"
        value={busca}
        onChange={(e) => setBusca(e.target.value)}
        placeholder="Buscar por serviço ou grupo — erp prod"
        aria-label="Buscar grupo ou serviço"
      />
      <div className="flex flex-col gap-0.5 text-small text-ink-3">
        <p>
          Grupo inteiro marcado liga o grupo: um monitor criado nele depois já avisa por este canal.
        </p>
        {buscando && <p>Com a busca, marcar um grupo marca só o que aparece abaixo dele.</p>}
      </div>
      {itens.length === 0 ? (
        <p className="text-small text-ink-3">Nenhum grupo ou serviço com "{busca.trim()}".</p>
      ) : (
        <CheckTree
          items={itens}
          open={buscando ? todasAsChaves(visivel) : abertos}
          onOpenChange={(key, abrir) => {
            const proximo = new Set(abertos);
            if (abrir) proximo.add(key);
            else proximo.delete(key);
            setAbertos(proximo);
          }}
          onToggle={alternar}
        />
      )}
    </div>
  );
}

/**
 * metaDoGrupo conta o que o grupo tem e diz quando o vínculo é com ele: é
 * a diferença entre avisar sobre os serviços de hoje e sobre os de amanhã.
 */
function metaDoGrupo(n: ServiceNode, sel: ReadonlySet<string>, peloGrupo: boolean): ReactNode {
  if (peloGrupo) return "o grupo, inclusive o que entrar depois";
  const servicos = chavesDoNo(n).filter((k) => k.startsWith("m"));
  if (servicos.length === 0) return "vazio";
  const marcados = servicos.filter((k) => sel.has(k)).length;
  const total = servicos.length;
  return (
    <span className="tabular">
      {marcados === 0
        ? `${total} ${total === 1 ? "serviço" : "serviços"}`
        : `${marcados} de ${total}`}
    </span>
  );
}

/** abertosComMarcado abre, na primeira vez, os grupos com algo marcado. */
function abertosComMarcado(arvore: ServiceNode[], sel: ReadonlySet<string>): Set<string> {
  const abertos = new Set<string>();
  const visitar = (n: ServiceNode) => {
    if (isCollapsible(n) && chavesDoNo(n).some((k) => sel.has(k) && k !== n.key)) abertos.add(n.key);
    n.children.forEach(visitar);
  };
  arvore.forEach(visitar);
  return abertos;
}

function todasAsChaves(nodes: ServiceNode[]): Set<string> {
  return new Set(
    nodes.flatMap(function chaves(n): string[] {
      return [n.key, ...n.children.flatMap(chaves)];
    }),
  );
}

function acharVisivel(nodes: ServiceNode[], key: string): ServiceNode | undefined {
  for (const n of nodes) {
    if (n.key === key) return n;
    const achado = acharVisivel(n.children, key);
    if (achado) return achado;
  }
  return undefined;
}
