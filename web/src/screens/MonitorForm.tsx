import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type Group, type MonitorInput, type MonitorType } from "../api/types";
import { Alert, Button, Field, Input, NumberInput, Select, TextLink } from "../components/ui";
import { fusoLocal, lerPush, montarPushConfig, type AgendaPush } from "../lib/push-config";
import {
  OPERADORES,
  aplicarAssercoes,
  lerAssercoes,
  linhaVazia,
  montarAssercoes,
  semValor,
  type LinhaAssercao,
  type Operador,
} from "../lib/json-assertions";
import { navigate } from "../lib/router";
import { ehProtocolo, lerProtocolo, montarProtocolo, protocoloVazio } from "../lib/protocol-config";
import { ProtocolFields } from "./ProtocolFields";

/**
 * Cadastro e edição de alvo.
 *
 * Os campos são nomeados pelo que a pessoa controla, não pelo que o
 * sistema guarda: "verificar a cada" em vez de "intervalo em
 * nanossegundos", "avisar como lento acima de" em vez de
 * "degraded_latency".
 */

type Tipo = {
  value: MonitorType;
  label: string;
  /** Exemplo de endereço, usado como placeholder. */
  alvo: string;
  /** O que este tipo de verificação faz. */
  ajuda: string;
  /** Que forma o endereço precisa ter. */
  formato: string;
};

const TIPOS: Tipo[] = [
  {
    value: "http",
    label: "Página ou API (HTTP)",
    alvo: "https://exemplo.com/health",
    ajuda: "Faz uma requisição e confere o código de resposta.",
    formato: "URL completa, com http:// ou https://.",
  },
  {
    value: "tcp",
    label: "Porta (TCP)",
    alvo: "banco.interno:5432",
    ajuda: "Abre uma conexão e fecha em seguida.",
    formato: "Máquina e porta, separadas por dois-pontos.",
  },
  {
    value: "tls",
    label: "Certificado (TLS)",
    alvo: "exemplo.com:443",
    ajuda: "Avisa antes de o certificado vencer.",
    formato: "Máquina e porta; 443 é o usual para HTTPS.",
  },
  {
    value: "dns",
    label: "Registro DNS",
    alvo: "exemplo.com",
    ajuda: "Confere se o nome resolve para onde deveria.",
    formato: "Apenas o nome, sem esquema nem porta.",
  },
  {
    value: "icmp",
    label: "Ping (ICMP)",
    alvo: "10.0.0.1",
    ajuda: "Envia pacotes e mede perda.",
    formato: "Nome ou endereço IP.",
  },
  {
    value: "push",
    label: "Sinal do próprio serviço",
    alvo: "",
    ajuda: "O serviço avisa que está vivo. Para tarefas agendadas e processos sem porta exposta.",
    formato: "",
  },
  {
    value: "redis",
    label: "Redis",
    alvo: "cache.interno:6379",
    ajuda: "Conecta e envia PING; pode conferir filas, conjuntos e memória.",
    formato: "Máquina e porta, separadas por dois-pontos.",
  },
  {
    value: "postgres",
    label: "PostgreSQL",
    alvo: "banco.interno:5432",
    ajuda: "Entra no banco e executa uma consulta somente leitura.",
    formato: "Máquina e porta, separadas por dois-pontos.",
  },
  {
    value: "mysql",
    label: "MySQL ou MariaDB",
    alvo: "banco.interno:3306",
    ajuda: "Entra no banco e executa uma consulta somente leitura.",
    formato: "Máquina e porta, separadas por dois-pontos.",
  },
];

const VAZIO: MonitorInput = {
  name: "",
  type: "http",
  target: "",
  interval_seconds: 60,
  timeout_seconds: 10,
  confirmation_threshold: 3,
  degraded_latency_ms: 0,
};

export function MonitorForm({ id }: { id?: number }) {
  const [form, setForm] = useState<MonitorInput>(VAZIO);
  const [erro, setErro] = useState<string | null>(null);
  const [campoErro, setCampoErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);
  const [pushToken, setPushToken] = useState("");
  const [protocolo, setProtocolo] = useState(protocoloVazio);
  const [agenda, setAgenda] = useState<AgendaPush>(() => lerPush(undefined, fusoLocal()).agenda);
  // Texto livre separado por vírgula, não uma lista de fichas: digitar
  // "produção, api" e sair do campo é mais rápido que apertar Enter a
  // cada etiqueta, e o servidor normaliza o que chegar.
  const [tags, setTags] = useState("");
  const [assercoes, setAssercoes] = useState<LinhaAssercao[]>([linhaVazia()]);
  const [grupos, setGrupos] = useState<Group[]>([]);

  const editando = id !== undefined;

  useEffect(() => {
    void api.listGroups().then(({ items }) => {
      setGrupos(items);
      // Quem tem escopo não pode deixar em "Sem grupo", que o servidor
      // recusaria; o primeiro ambiente é um padrão que se troca fácil.
      if (id === undefined) {
        // Vindo da tela de um grupo, o cadastro já nasce nele: quem clica
        // em "Novo monitor" olhando para "RPA · prod" quer criar ali.
        const pedido = Number(new URLSearchParams(window.location.search).get("grupo"));
        const doGrupo = items.find((g) => g.id === pedido && folha(g, items));
        const primeiro = doGrupo ?? items.find((g) => folha(g, items));
        setForm((f) => (f.group_id == null && primeiro ? { ...f, group_id: primeiro.id } : f));
      }
    });
  }, [id]);
  const tipo = TIPOS.find((t) => t.value === form.type)!;

  useEffect(() => {
    if (id === undefined) return;

    void api.getMonitor(id).then((m) => {
      setForm({
        name: m.name,
        type: m.type,
        target: m.target,
        interval_seconds: m.interval_seconds,
        timeout_seconds: m.timeout_seconds,
        confirmation_threshold: m.confirmation_threshold,
        degraded_latency_ms: m.degraded_latency_ms,
        config: m.config,
        group_id: m.group_id,
      });
      const push = lerPush(m.config, fusoLocal());
      setPushToken(push.token);
      setAgenda(push.agenda);
      if (ehProtocolo(m.type)) setProtocolo(lerProtocolo(m.type, m.config));
      setTags((m.tags ?? []).join(", "));
      setAssercoes(lerAssercoes(m.config));
    });
  }, [id]);

  // Monitor push precisa de um segredo, e gerá-lo aqui evita que alguém
  // escolha "1234" por conveniência — quem adivinha o token mantém o
  // monitor saudável enquanto o serviço real está parado.
  useEffect(() => {
    if (form.type === "push" && !pushToken) setPushToken(gerarToken());
  }, [form.type, pushToken]);

  function set<K extends keyof MonitorInput>(key: K, value: MonitorInput[K]) {
    setForm((f) => ({ ...f, [key]: value }));
  }

  async function enviar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    setCampoErro(null);

    let config = form.config;
    if (form.type === "push") {
      config = montarPushConfig(pushToken, agenda);
    } else if (form.type === "http") {
      // Recusa antes de enviar para o erro apontar a linha: o servidor
      // responderia "config inválida", sem dizer qual asserção.
      const montado = montarAssercoes(assercoes);
      if (montado.erro) {
        setErro(montado.erro);
        setCampoErro("json_assertions");
        setEnviando(false);
        return;
      }
      config = aplicarAssercoes(form.config, montado.assercoes!);
    } else if (ehProtocolo(form.type)) {
      const r = montarProtocolo(form.type, protocolo);
      if (r.erro) {
        setErro(r.erro.msg);
        setCampoErro(r.erro.campo);
        setEnviando(false);
        return;
      }
      config = r.config;
    }

    const payload: MonitorInput = {
      ...form,
      tags: tags.split(",").map((t) => t.trim()).filter(Boolean),
      config,
    };

    try {
      const salvo = editando
        ? await api.updateMonitor(id!, payload)
        : await api.createMonitor(payload);
      navigate({ name: "monitor", id: salvo.id });
    } catch (e) {
      if (e instanceof ApiError) {
        setErro(e.message);
        setCampoErro(e.field ?? null);
      } else {
        setErro("Não foi possível salvar. Verifique se o servidor está no ar.");
      }
    } finally {
      setEnviando(false);
    }
  }

  const erroDe = (campo: string) => (campoErro === campo ? erro ?? undefined : undefined);
  const mudarAgenda = <K extends keyof AgendaPush>(key: K, value: AgendaPush[K]) =>
    setAgenda((a) => ({ ...a, [key]: value }));

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-6 px-5 py-6">
      <nav>
        <TextLink onClick={() => history.back()}>← voltar</TextLink>
      </nav>

      <h1 className="text-title font-semibold tracking-tight">
        {editando ? "Editar monitor" : "Novo monitor"}
      </h1>

      {/* Erro de config não tem campo próprio na tela: a configuração é
          montada a partir de vários. O push é a exceção, porque o erro
          dele aparece junto da agenda, e repeti-lo aqui seria ruído. */}
      {erro && (!campoErro || (campoErro === "config" && form.type !== "push")) && (
        <Alert>{erro}</Alert>
      )}

      <form onSubmit={enviar} className="flex flex-col gap-4">
        <Field label="nome" hint="Como este alvo aparece no painel." error={erroDe("name")}>
          <Input
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="api de produção"
            autoFocus
            required
          />
        </Field>

        {grupos.length > 0 && (
          <Field
            label="grupo"
            hint="Só ambientes (ou sistemas sem ambientes) recebem monitores."
            error={erroDe("group_id")}
          >
            <Select
              value={form.group_id ?? ""}
              onChange={(e) => set("group_id", e.target.value ? Number(e.target.value) : null)}
            >
              <option value="">Sem grupo</option>
              {grupos
                .filter((g) => folha(g, grupos))
                .map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.path}
                  </option>
                ))}
            </Select>
          </Field>
        )}

        <Field label="o que verificar" hint={tipo.ajuda} error={erroDe("type")}>
          <Select
            value={form.type}
            onChange={(e) => set("type", e.target.value as MonitorType)}
          >
            {TIPOS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
        </Field>

        {form.type !== "push" && (
          <Field label="endereço" hint={tipo.formato} error={erroDe("target")}>
            <Input
              value={form.target}
              onChange={(e) => set("target", e.target.value)}
              placeholder={tipo.alvo}
              className="tabular"
              required
            />
          </Field>
        )}

        {form.type === "push" && (
          <Field
            label="endereço para o seu serviço chamar"
            hint="Faça o serviço bater neste endereço a cada ciclo. Sem sinal na janela, o monitor acusa parada."
          >
            <code className="block overflow-x-auto rounded-sm border border-line-strong bg-sunken px-2.5 py-2 text-small">
              {`${location.origin}/api/v1/push/${pushToken}`}
            </code>
          </Field>
        )}

        {ehProtocolo(form.type) && (
          <ProtocolFields
            tipo={form.type}
            valor={protocolo}
            onChange={setProtocolo}
            erroDe={erroDe}
          />
        )}

        {form.type === "push" && (
          <>
            {/* A agenda serve à rotina de horário fixo: uma tarefa das 09:02
                e das 18:02 não cabe em intervalo nenhum. O erro de expressão
                volta do servidor e aparece aqui, no cadastro; a próxima
                execução esperada aparece no detalhe do monitor. */}
            <Field
              label="agenda (opcional)"
              hint="Expressão cron de 5 campos, como no crontab. Vazio espera o sinal a cada intervalo."
              error={erroDe("config")}
            >
              <Input
                value={agenda.schedule}
                onChange={(e) => mudarAgenda("schedule", e.target.value)}
                placeholder="2 9,18 * * *"
                className="tabular"
              />
            </Field>

            <div className="grid grid-cols-2 gap-4">
              <Field
                label="fuso da agenda"
                hint="Fuso IANA. O horário de verão desloca a execução, por isso é explícito."
              >
                <Input
                  value={agenda.timezone}
                  onChange={(e) => mudarAgenda("timezone", e.target.value)}
                  placeholder="America/Sao_Paulo"
                  disabled={!agenda.schedule.trim()}
                />
              </Field>

              <Field
                label="folga"
                hint="segundos de atraso tolerados; zero usa o intervalo, e com agenda no mínimo 5 minutos"
              >
                <NumberInput
                  min={0}
                  value={agenda.folga}
                  onChange={(e) => mudarAgenda("folga", Number(e.target.value))}
                />
              </Field>
            </div>
          </>
        )}

        <div className="grid grid-cols-2 gap-4">
          <Field label="verificar a cada" hint="segundos" error={erroDe("interval")}>
            <NumberInput
              min={5}
              value={form.interval_seconds}
              onChange={(e) => set("interval_seconds", Number(e.target.value))}
              required
            />
          </Field>

          <Field
            label="desistir depois de"
            hint="segundos; precisa ser menor que o intervalo"
            error={erroDe("timeout")}
          >
            <NumberInput
              min={1}
              value={form.timeout_seconds}
              onChange={(e) => set("timeout_seconds", Number(e.target.value))}
              required
            />
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <Field
            label="acusar queda após"
            hint="falhas seguidas; evita alarme por soluço de rede"
            error={erroDe("confirmation_threshold")}
          >
            <NumberInput
              min={1}
              value={form.confirmation_threshold}
              onChange={(e) => set("confirmation_threshold", Number(e.target.value))}
            />
          </Field>

          <Field
            label="marcar como lento acima de"
            hint="milissegundos; zero desliga"
            error={erroDe("degraded_latency")}
          >
            <NumberInput
              min={0}
              value={form.degraded_latency_ms}
              onChange={(e) => set("degraded_latency_ms", Number(e.target.value))}
            />
          </Field>
        </div>

        {form.type === "http" && (
          <AssercoesJSON
            linhas={assercoes}
            onLinhas={setAssercoes}
            erro={erroDe("json_assertions") ?? erroDe("config")}
          />
        )}

        <Field
          label="etiquetas"
          hint="Separadas por vírgula. Agrupam o painel — produção, homolog, o time responsável."
          error={erroDe("tags")}
        >
          <Input
            value={tags}
            onChange={(e) => setTags(e.target.value)}
            placeholder="produção, plataforma"
          />
        </Field>

        <div className="flex gap-2 pt-2">
          <Button type="submit" variant="primary" disabled={enviando}>
            {enviando ? "Salvando" : editando ? "Salvar alterações" : "Criar monitor"}
          </Button>
          <Button type="button" onClick={() => history.back()}>
            Cancelar
          </Button>
        </div>
      </form>
    </div>
  );
}

/**
 * AssercoesJSON edita as condições sobre o corpo JSON da resposta.
 *
 * Fechado por padrão, como os campos avançados do canal: a maioria dos
 * alvos só precisa do código de resposta. Abre sozinho quando o monitor já
 * tem asserções, para quem edita ver o que está valendo.
 */
function AssercoesJSON({
  linhas,
  onLinhas,
  erro,
}: {
  linhas: LinhaAssercao[];
  onLinhas: (linhas: LinhaAssercao[]) => void;
  erro?: string;
}) {
  const temAlguma = linhas.some((l) => l.caminho.trim() !== "");
  // null enquanto a pessoa não mexe: aí a seção segue o conteúdo. Depois
  // do primeiro clique, a escolha dela vale — exceto com erro, que nunca
  // fica escondido.
  const [aberto, setAberto] = useState<boolean | null>(null);
  const visivel = (aberto ?? temAlguma) || erro !== undefined;

  function alterar(i: number, campo: keyof LinhaAssercao, valor: string) {
    const proximo = linhas.map((l, j) => (j === i ? { ...l, [campo]: valor } : l));

    // Uma linha vazia sempre sobra no fim, para nunca haver um passo entre
    // querer outra asserção e ter onde digitá-la.
    const ultima = proximo[proximo.length - 1];
    if (!ultima || ultima.caminho.trim() !== "") proximo.push(linhaVazia());
    onLinhas(proximo);
  }

  function remover(i: number) {
    const proximo = linhas.filter((_, j) => j !== i);
    onLinhas(proximo.length > 0 ? proximo : [linhaVazia()]);
  }

  return (
    <div className="flex flex-col gap-4 border-t border-line pt-4">
      <TextLink onClick={() => setAberto(!visivel)} className="self-start">
        {visivel ? "− " : "+ "}
        conferir valores do corpo JSON
      </TextLink>

      {visivel && (
        <div className="flex flex-col gap-1.5">
          <span className="text-small text-ink-3">
            Todas precisam valer. O caminho começa em <code className="tabular">$</code>:{" "}
            <code className="tabular">$.db</code>, <code className="tabular">$.filas[0].atraso</code>
            , <code className="tabular">$["chave com espaço"]</code>. No valor, números,{" "}
            <code className="tabular">true</code>, <code className="tabular">false</code> e{" "}
            <code className="tabular">null</code> valem como tais; o resto é texto — use aspas para
            comparar <code className="tabular">"100"</code> como texto.
          </span>

          <ul className="mt-1 flex flex-col gap-2">
            {linhas.map((linha, i) => (
              <li key={i} className="flex gap-2">
                <Input
                  value={linha.caminho}
                  onChange={(e) => alterar(i, "caminho", e.target.value)}
                  placeholder="$.queue_lag"
                  className="tabular flex-1"
                  aria-label={`caminho da asserção ${i + 1}`}
                />
                <div className="w-40 shrink-0">
                  <Select
                    value={linha.operador}
                    onChange={(e) => alterar(i, "operador", e.target.value as Operador)}
                    aria-label={`operador da asserção ${i + 1}`}
                  >
                    {OPERADORES.map((o) => (
                      <option key={o.value} value={o.value}>
                        {o.label}
                      </option>
                    ))}
                  </Select>
                </div>
                {!semValor(linha.operador) && (
                  <Input
                    value={linha.valor}
                    onChange={(e) => alterar(i, "valor", e.target.value)}
                    placeholder="100"
                    className="tabular flex-1"
                    aria-label={`valor da asserção ${i + 1}`}
                  />
                )}
                <Button
                  type="button"
                  size="sm"
                  onClick={() => remover(i)}
                  className="shrink-0"
                  aria-label={`remover asserção ${i + 1}`}
                >
                  ×
                </Button>
              </li>
            ))}
          </ul>

          {erro && (
            <span role="alert" className="text-small text-down">
              {erro}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

/** gerarToken produz um segredo imprevisível com a API do navegador. */
/** folha diz se o grupo pode receber monitores: não tem ambientes. */
function folha(g: Group, todos: Group[]): boolean {
  return !todos.some((x) => x.parent_id === g.id);
}

function gerarToken(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24));

  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}
