import { useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type MonitorInput, type MonitorType } from "../api/types";
import { Alert, Button, Field, Input, NumberInput, Select, TextLink } from "../components/ui";
import { navigate } from "../lib/router";

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

  const editando = id !== undefined;
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
      });
      setPushToken(String(m.config?.["token"] ?? ""));
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

    const payload: MonitorInput = {
      ...form,
      config: form.type === "push" ? { token: pushToken } : form.config,
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

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-6 px-5 py-6">
      <nav>
        <TextLink onClick={() => history.back()}>← voltar</TextLink>
      </nav>

      <h1 className="text-title font-semibold tracking-tight">
        {editando ? "Editar monitor" : "Novo monitor"}
      </h1>

      {erro && !campoErro && <Alert>{erro}</Alert>}

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

/** gerarToken produz um segredo imprevisível com a API do navegador. */
function gerarToken(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24));

  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}
