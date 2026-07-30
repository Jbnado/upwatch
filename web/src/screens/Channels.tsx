import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Channel, type ChannelType } from "../api/types";
import {
  Alert,
  Button,
  Checkbox,
  Field,
  InlineLink,
  Input,
  Loading,
  Nothing,
  Select,
  Textarea,
  TextLink,
} from "../components/ui";
import { montarConfig, type LinhaCabecalho } from "../lib/channel-config";

/**
 * Canais de aviso.
 *
 * O botão de testar é o elemento central da tela: sem ele, a única forma
 * de descobrir que o alerta não chega seria esperar uma queda de verdade —
 * e nesse momento já é tarde.
 */

const TIPOS: Record<ChannelType, { label: string; ajuda: string; exemplo: string }> = {
  discord: {
    label: "Discord",
    ajuda: "Cole a URL do webhook do canal (Editar canal → Integrações → Webhooks).",
    exemplo: "https://discord.com/api/webhooks/…",
  },
  slack: {
    label: "Slack",
    ajuda: "Cole a URL do webhook de entrada do app do Slack.",
    exemplo: "https://hooks.slack.com/services/…",
  },
  webhook: {
    label: "Webhook",
    ajuda: "Recebe um JSON com os campos crus do evento, para integrar com o que você quiser.",
    exemplo: "https://seu-servico.exemplo/alertas",
  },
};

export function Channels() {
  const [canais, setCanais] = useState<Channel[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [testando, setTestando] = useState<number | null>(null);
  const [resultado, setResultado] = useState<{ id: number; ok: boolean; texto: string } | null>(null);

  async function carregar() {
    const { items } = await api.listChannels();
    setCanais(items);
  }

  useEffect(() => {
    void carregar();
  }, []);

  async function testar(canal: Channel) {
    setTestando(canal.id);
    setResultado(null);

    try {
      await api.testChannel(canal.id);
      setResultado({ id: canal.id, ok: true, texto: "mensagem entregue" });
    } catch (e) {
      setResultado({
        id: canal.id,
        ok: false,
        texto: e instanceof ApiError ? e.message : "não foi possível entregar",
      });
    } finally {
      setTestando(null);
    }
  }

  async function remover(canal: Channel) {
    if (!confirm(`Remover "${canal.name}"? Os monitores vinculados param de avisar por ele.`)) {
      return;
    }
    await api.deleteChannel(canal.id);
    await carregar();
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Canais de aviso</h2>
        <p className="text-body text-ink-2">
          Para onde o UpWatch manda o alerta quando um alvo cai. Vincule cada canal aos
          monitores na tela do próprio monitor.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      <NewChannel
        onCreated={async () => {
          setErro(null);
          await carregar();
        }}
        onError={setErro}
      />

      {canais === null ? (
        <Loading what="canais" />
      ) : canais.length === 0 ? (
        <Nothing hint="Sem canal, o UpWatch registra as quedas mas não avisa ninguém.">
          Nenhum canal cadastrado.
        </Nothing>
      ) : (
        <ul className="border-t border-line">
          {canais.map((canal) => (
            <li key={canal.id} className="flex flex-col gap-1.5 border-b border-line py-2.5">
              <div className="flex items-center justify-between gap-4">
                <span className="flex min-w-0 items-baseline gap-2">
                  <span className="truncate text-body font-medium">{canal.name}</span>
                  <span className="eyebrow">{TIPOS[canal.type]?.label ?? canal.type}</span>
                  {!canal.enabled && (
                    <span className="text-small text-ink-3">desligado</span>
                  )}
                </span>

                {/* Ação em linha usa o tamanho menor: o botão de altura
                    cheia esticaria cada item e a lista perderia densidade. */}
                <span className="flex shrink-0 items-center gap-2">
                  <Button size="sm" onClick={() => testar(canal)} disabled={testando === canal.id}>
                    {testando === canal.id ? "Enviando" : "Testar"}
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => remover(canal)}>
                    Remover
                  </Button>
                </span>
              </div>

              {/* O resultado do teste aparece junto do canal testado, não num
                  aviso global: com vários canais, saber qual respondeu é a
                  informação que importa. */}
              {resultado?.id === canal.id && (
                <span className={`text-small ${resultado.ok ? "text-up" : "text-down"}`}>
                  {resultado.texto}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function NewChannel({
  onCreated,
  onError,
}: {
  onCreated: () => Promise<void>;
  onError: (msg: string) => void;
}) {
  const [tipo, setTipo] = useState<ChannelType>("discord");
  const [nome, setNome] = useState("");
  const [url, setUrl] = useState("");
  const [cabecalhos, setCabecalhos] = useState<LinhaCabecalho[]>([{ nome: "", valor: "" }]);
  const [mapeamento, setMapeamento] = useState("");
  const [avancado, setAvancado] = useState(false);
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setCampoErro(null);

    // A montagem recusa antes de enviar para o erro aparecer embaixo do
    // campo certo: o servidor responderia "config inválida", e a tela não
    // teria como saber se o problema é a URL ou o mapeamento.
    const { config, erro } = montarConfig({ url, cabecalhos, mapeamento });
    if (erro) {
      setCampoErro(erro);
      return;
    }

    setEnviando(true);

    try {
      await api.createChannel({ name: nome, type: tipo, config: config! });
      setNome("");
      setUrl("");
      setCabecalhos([{ nome: "", valor: "" }]);
      setMapeamento("");
      await onCreated();
    } catch (e) {
      if (e instanceof ApiError && e.field) {
        setCampoErro({ campo: e.field, msg: e.message });
      } else if (e instanceof ApiError) {
        onError(e.message);
      } else {
        onError("Não foi possível criar o canal.");
      }
    } finally {
      setEnviando(false);
    }
  }

  const info = TIPOS[tipo];
  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);

  return (
    <form onSubmit={criar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
      <div className="grid gap-4 sm:grid-cols-[160px_minmax(0,1fr)]">
        <Field label="onde avisar">
          <Select value={tipo} onChange={(e) => setTipo(e.target.value as ChannelType)}>
            {(Object.keys(TIPOS) as ChannelType[]).map((t) => (
              <option key={t} value={t}>
                {TIPOS[t].label}
              </option>
            ))}
          </Select>
        </Field>

        <Field label="nome" hint="Como este canal aparece na lista." error={erroDe("name")}>
          <Input
            value={nome}
            onChange={(e) => setNome(e.target.value)}
            placeholder="plantão de infra"
            required
          />
        </Field>
      </div>

      <Field label="endereço do webhook" hint={info.ajuda} error={erroDe("config")}>
        <Input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder={info.exemplo}
          className="tabular"
          type="url"
          required
        />
      </Field>

      {/* Só o canal genérico. Discord e Slack têm corpo ditado pelo
          destino, e oferecer os campos ali seria oferecer um jeito de
          quebrar a integração — o servidor recusa, mas melhor nem pedir. */}
      {tipo === "webhook" && (
        <Avancado
          aberto={avancado}
          onAlternar={() => setAvancado((v) => !v)}
          cabecalhos={cabecalhos}
          onCabecalhos={setCabecalhos}
          mapeamento={mapeamento}
          onMapeamento={setMapeamento}
          erroMapeamento={erroDe("mapeamento")}
        />
      )}

      <Button type="submit" variant="primary" disabled={enviando} className="self-start">
        {enviando ? "Salvando" : "Adicionar canal"}
      </Button>
    </form>
  );
}

/** Os marcadores que um mapeamento pode usar, com o que cada um entrega. */
const MARCADORES: [string, string][] = [
  ["$monitor", "nome do alvo"],
  ["$monitor_id", "identificador, como número"],
  ["$target", "endereço verificado"],
  ["$status", "up, down, degraded ou unknown"],
  ["$previous_status", "o estado anterior"],
  ["$message", "causa observada, quando houver"],
  ["$at", "instante da mudança"],
  ["$duration_seconds", "duração do estado anterior, como número"],
  ["$text", "a frase pronta do aviso"],
];

const EXEMPLO_MAPEAMENTO = `{
  "event": "$status",
  "service": "$monitor",
  "detail": "$message"
}`;

/**
 * Avancado reúne o que quase ninguém precisa e alguém precisa muito.
 *
 * Fechado por padrão porque a maioria dos destinos aceita o corpo padrão,
 * e três campos a mais no caminho de todo mundo cobram o preço da exceção
 * de quem não tem a exceção. Quem precisa, abre uma vez.
 */
function Avancado({
  aberto,
  onAlternar,
  cabecalhos,
  onCabecalhos,
  mapeamento,
  onMapeamento,
  erroMapeamento,
}: {
  aberto: boolean;
  onAlternar: () => void;
  cabecalhos: LinhaCabecalho[];
  onCabecalhos: (linhas: LinhaCabecalho[]) => void;
  mapeamento: string;
  onMapeamento: (v: string) => void;
  erroMapeamento?: string;
}) {
  function alterar(i: number, campo: keyof LinhaCabecalho, valor: string) {
    const proximo = cabecalhos.map((l, j) => (j === i ? { ...l, [campo]: valor } : l));

    // Uma linha vazia sempre sobra no fim, para nunca haver um passo entre
    // querer outro cabeçalho e ter onde digitá-lo.
    const ultima = proximo[proximo.length - 1];
    if (!ultima || ultima.nome.trim() !== "" || ultima.valor.trim() !== "") {
      proximo.push({ nome: "", valor: "" });
    }
    onCabecalhos(proximo);
  }

  function remover(i: number) {
    const proximo = cabecalhos.filter((_, j) => j !== i);
    onCabecalhos(proximo.length > 0 ? proximo : [{ nome: "", valor: "" }]);
  }

  return (
    <div className="flex flex-col gap-4 border-t border-line pt-4">
      <TextLink onClick={onAlternar} className="self-start">
        {aberto ? "− " : "+ "}
        cabeçalhos e formato do corpo
      </TextLink>

      {aberto && (
        <>
          <div className="flex flex-col gap-1.5">
            <span className="eyebrow">cabeçalhos</span>
            <span className="text-small text-ink-3">
              Para destinos que exigem chave própria. O valor é gravado como você digitar.
            </span>

            <ul className="mt-1 flex flex-col gap-2">
              {cabecalhos.map((linha, i) => (
                <li key={i} className="flex gap-2">
                  <Input
                    value={linha.nome}
                    onChange={(e) => alterar(i, "nome", e.target.value)}
                    placeholder="Authorization"
                    className="tabular flex-1"
                    aria-label={`nome do cabeçalho ${i + 1}`}
                  />
                  <Input
                    value={linha.valor}
                    onChange={(e) => alterar(i, "valor", e.target.value)}
                    placeholder="Bearer …"
                    className="tabular flex-1"
                    aria-label={`valor do cabeçalho ${i + 1}`}
                  />
                  <Button
                    type="button"
                    size="sm"
                    onClick={() => remover(i)}
                    className="shrink-0"
                    aria-label={`remover cabeçalho ${i + 1}`}
                  >
                    ×
                  </Button>
                </li>
              ))}
            </ul>
          </div>

          <Field
            label="formato do corpo"
            hint="Em branco, o corpo é o JSON padrão com todos os campos."
            error={erroMapeamento}
          >
            <Textarea
              value={mapeamento}
              onChange={(e) => onMapeamento(e.target.value)}
              placeholder={EXEMPLO_MAPEAMENTO}
              className="tabular text-small"
            />
          </Field>

          {/* A lista fica ao lado do campo, e não no README: um recurso
              cuja única documentação está fora da tela é um recurso que
              ninguém encontra. */}
          <div className="flex flex-col gap-1.5">
            <span className="eyebrow">marcadores</span>
            <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
              {MARCADORES.map(([marcador, oQue]) => (
                <div key={marcador} className="contents">
                  <dt className="tabular text-small text-ink">{marcador}</dt>
                  <dd className="text-small text-ink-3">{oQue}</dd>
                </div>
              ))}
            </dl>
            <span className="text-small text-ink-3">
              Sozinho, o marcador entrega o valor com o tipo dele — número continua número.
              Dentro de um texto, compõe a frase. Use <code className="tabular">$$</code> para um
              cifrão literal.
            </span>
          </div>
        </>
      )}
    </div>
  );
}

/**
 * MonitorChannels vincula canais a um monitor.
 *
 * Vive na tela do monitor porque a pergunta é "quem quero que saiba quando
 * este alvo cair?" — não "quais canais existem?".
 */
export function MonitorChannels({ monitorID }: { monitorID: number }) {
  const [todos, setTodos] = useState<Channel[]>([]);
  const [vinculados, setVinculados] = useState<Set<number>>(new Set());
  const [carregando, setCarregando] = useState(true);

  async function carregar() {
    const [lista, doMonitor] = await Promise.all([
      api.listChannels(),
      api.monitorChannels(monitorID),
    ]);
    setTodos(lista.items);
    setVinculados(new Set(doMonitor.items.map((c) => c.id)));
    setCarregando(false);
  }

  useEffect(() => {
    void carregar();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [monitorID]);

  async function alternar(canal: Channel) {
    const ligado = vinculados.has(canal.id);

    // Atualiza a caixa antes da resposta: o vínculo é idempotente e o
    // retorno é imediato, então esperar só faria o clique parecer travado.
    setVinculados((atual) => {
      const proximo = new Set(atual);
      if (ligado) proximo.delete(canal.id);
      else proximo.add(canal.id);
      return proximo;
    });

    try {
      if (ligado) await api.unlinkChannel(monitorID, canal.id);
      else await api.linkChannel(monitorID, canal.id);
    } catch {
      // Falhou: recarrega para a tela voltar a refletir o servidor em vez
      // de mostrar um estado que não existe.
      await carregar();
    }
  }

  if (carregando) return <Loading what="canais" />;

  if (todos.length === 0) {
    return (
      <p className="text-body text-ink-2">
        Nenhum canal cadastrado. Crie um em{" "}
        <InlineLink to={{ name: "settings" }}>ajustes</InlineLink> para receber alerta quando este
        alvo cair.
      </p>
    );
  }

  // -mx-1 alinha o texto das caixas com o resto da coluna: o Checkbox tem
  // preenchimento próprio para a área de clique cobrir o rótulo inteiro,
  // e sem isso a lista apareceria recuada em relação ao título da seção.
  return (
    <ul className="-mx-1 flex flex-col">
      {todos.map((canal) => (
        <li key={canal.id}>
          <Checkbox checked={vinculados.has(canal.id)} onChange={() => alternar(canal)}>
            <span>{canal.name}</span>
            <span className="eyebrow">{TIPOS[canal.type]?.label ?? canal.type}</span>
            {!canal.enabled && <span className="text-small text-ink-3">desligado</span>}
          </Checkbox>
        </li>
      ))}
    </ul>
  );
}
