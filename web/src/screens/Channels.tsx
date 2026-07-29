import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Channel, type ChannelType } from "../api/types";
import { Alert, Button, Empty, Field, Input, Select } from "../components/ui";

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
      <div className="flex flex-col gap-1">
        <h2 className="text-[15px] font-medium">Canais de aviso</h2>
        <p className="text-[13px] text-ink-2">
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
        <p className="eyebrow">carregando</p>
      ) : canais.length === 0 ? (
        <Empty
          title="Nenhum canal cadastrado"
          description="Sem canal, o UpWatch registra as quedas mas não avisa ninguém."
        />
      ) : (
        <ul className="border-t border-line">
          {canais.map((canal) => (
            <li key={canal.id} className="flex flex-col gap-1.5 border-b border-line py-2.5">
              <div className="flex items-baseline justify-between gap-4">
                <span className="flex min-w-0 items-baseline gap-2">
                  <span className="truncate text-[13px] font-medium">{canal.name}</span>
                  <span className="eyebrow">{TIPOS[canal.type]?.label ?? canal.type}</span>
                  {!canal.enabled && (
                    <span className="text-[12px] text-ink-3">desligado</span>
                  )}
                </span>

                <span className="flex shrink-0 items-center gap-2">
                  <Button onClick={() => testar(canal)} disabled={testando === canal.id}>
                    {testando === canal.id ? "Enviando" : "Testar"}
                  </Button>
                  <Button variant="danger" onClick={() => remover(canal)}>
                    Remover
                  </Button>
                </span>
              </div>

              {/* O resultado do teste aparece junto do canal testado, não num
                  aviso global: com vários canais, saber qual respondeu é a
                  informação que importa. */}
              {resultado?.id === canal.id && (
                <span className={`text-[12px] ${resultado.ok ? "text-up" : "text-down"}`}>
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
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);

  async function criar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setCampoErro(null);

    try {
      await api.createChannel({ name: nome, type: tipo, config: { url } });
      setNome("");
      setUrl("");
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
    <form onSubmit={criar} className="flex flex-col gap-4 border border-line-strong p-4">
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

      <Button type="submit" variant="primary" disabled={enviando} className="self-start">
        {enviando ? "Salvando" : "Adicionar canal"}
      </Button>
    </form>
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

  if (carregando) return <p className="eyebrow">carregando</p>;

  if (todos.length === 0) {
    return (
      <p className="text-[13px] text-ink-2">
        Nenhum canal cadastrado. Crie um em{" "}
        <a href="/settings" className="underline">
          ajustes
        </a>{" "}
        para receber alerta quando este alvo cair.
      </p>
    );
  }

  return (
    <ul className="flex flex-col gap-1.5">
      {todos.map((canal) => (
        <li key={canal.id}>
          <label className="flex cursor-pointer items-center gap-2 text-[13px]">
            <input
              type="checkbox"
              checked={vinculados.has(canal.id)}
              onChange={() => alternar(canal)}
              className="accent-ink"
            />
            <span>{canal.name}</span>
            <span className="eyebrow">{TIPOS[canal.type]?.label ?? canal.type}</span>
            {!canal.enabled && <span className="text-[12px] text-ink-3">desligado</span>}
          </label>
        </li>
      ))}
    </ul>
  );
}
