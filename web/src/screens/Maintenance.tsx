import { useEffect, useMemo, useState, type FormEvent } from "react";
import { api } from "../api/client";
import {
  ApiError,
  type Group,
  type MaintenanceInput,
  type MaintenanceWindow,
  type Monitor,
  type Recurrence,
} from "../api/types";
import {
  Alert,
  Button,
  Field,
  FormActions,
  Input,
  Loading,
  NewItem,
  Nothing,
  Select,
  Textarea,
} from "../components/ui";
import {
  ESTADO_LABEL,
  RECORRENCIA_LABEL,
  alvos,
  alvosDaSelecao,
  doCampo,
  fusoDoNavegador,
  ordenar,
  padraoAgora,
  paraCampo,
  quando,
  selecaoDosAlvos,
} from "../lib/maintenance";
import { buildServiceTree } from "../lib/service-tree";
import { MonitorTree } from "../components/MonitorTree";

/**
 * Janelas de manutenção.
 *
 * "RPA/prod em manutenção hoje das 19h às 19h30": o deploy derruba a API
 * por um minuto, e sem a janela o aviso acendia para o time inteiro. A
 * verificação continua; só o aviso fica calado, e o histórico registra a
 * manutenção.
 */
export function Maintenance() {
  const [janelas, setJanelas] = useState<MaintenanceWindow[] | null>(null);
  const [grupos, setGrupos] = useState<Group[]>([]);
  const [monitores, setMonitores] = useState<Monitor[]>([]);
  const [erro, setErro] = useState<string | null>(null);
  const [editando, setEditando] = useState<number | null>(null);

  async function carregar() {
    const { items } = await api.maintenanceWindows();
    setJanelas(ordenar(items));
  }

  useEffect(() => {
    void carregar();
    void api.listGroups().then(({ items }) => setGrupos(items));
    void api.listMonitors({ limit: 500 }).then(({ items }) => setMonitores(items));
  }, []);

  async function agir(fn: () => Promise<unknown>) {
    setErro(null);
    try {
      await fn();
      await carregar();
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível concluir.");
    }
  }

  function encerrar(j: MaintenanceWindow) {
    // Na agendada, encerrar é cancelar; o texto diz isso para ninguém
    // achar que vai apagar uma em curso por engano.
    const pergunta =
      j.state === "active"
        ? `Encerrar "${j.title}" agora? Os avisos voltam na hora.`
        : `Cancelar "${j.title}"? ${j.recurrence === "none" ? "Ela" : "Nenhuma ocorrência"} não vai mais acontecer.`;
    if (!confirm(pergunta)) return;
    void agir(() => api.endMaintenance(j.id));
  }

  function remover(j: MaintenanceWindow) {
    if (!confirm(`Remover "${j.title}"? As verificações já gravadas continuam dizendo que houve manutenção.`)) {
      return;
    }
    void agir(() => api.deleteMaintenance(j.id));
  }

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Manutenção</h2>
        <p className="text-body text-ink-2">
          Durante a janela as verificações continuam, mas a falha não abre incidente, não avisa e
          não conta como queda. Se o alvo continuar fora quando a janela acabar, ele avisa — uma vez,
          depois das confirmações de sempre.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {janelas === null ? (
        <Loading what="janelas de manutenção" />
      ) : janelas.length === 0 ? (
        <Nothing hint="Declare a janela antes do deploy, e o aviso não acende para o time por uma queda esperada.">
          Nenhuma janela de manutenção.
        </Nothing>
      ) : (
        <ul className="border-t border-line">
          {janelas.map((j) =>
            editando === j.id ? (
              <li key={j.id} className="border-b border-line py-2.5">
                <FormJanela
                  inicial={j}
                  grupos={grupos}
                  monitores={monitores}
                  submit="Salvar"
                  onSave={(input) => api.updateMaintenance(j.id, input)}
                  onDone={async () => {
                    setEditando(null);
                    await carregar();
                  }}
                  onCancel={() => setEditando(null)}
                />
              </li>
            ) : (
              <Linha
                key={j.id}
                j={j}
                alvos={alvos(j, grupos, monitores)}
                onEditar={() => setEditando(j.id)}
                onEncerrar={() => encerrar(j)}
                onRemover={() => remover(j)}
              />
            ),
          )}
        </ul>
      )}

      <NewItem label="Nova janela">
        {(fechar) => (
          <FormJanela
            grupos={grupos}
            monitores={monitores}
            submit="Declarar janela"
            onSave={(input) => api.createMaintenance(input)}
            onDone={async () => {
              fechar();
              await carregar();
            }}
            onCancel={fechar}
          />
        )}
      </NewItem>
    </section>
  );
}

function Linha({
  j,
  alvos,
  onEditar,
  onEncerrar,
  onRemover,
}: {
  j: MaintenanceWindow;
  alvos: string[];
  onEditar: () => void;
  onEncerrar: () => void;
  onRemover: () => void;
}) {
  const ativa = j.state === "active";
  return (
    <li className="flex flex-col gap-1.5 border-b border-line py-2.5">
      <div className="flex items-start justify-between gap-4">
        <span className="flex min-w-0 flex-col gap-1">
          <span className="flex items-baseline gap-2">
            <span className="truncate text-body font-medium">{j.title}</span>
            <span className={`text-small ${ativa ? "font-medium text-maint" : "text-ink-3"}`}>
              {ESTADO_LABEL[j.state]}
            </span>
          </span>
          <span className="tabular text-small text-ink-2">{quando(j)}</span>
          <span className="text-small text-ink-3">
            {alvos.length > 0 ? alvos.join(", ") : "sem alvo — o monitor ou grupo foi apagado"}
          </span>
        </span>

        <span className="flex shrink-0 flex-wrap items-center justify-end gap-2">
          {j.state !== "finished" && (
            <Button size="sm" onClick={onEncerrar}>
              {ativa ? "Encerrar agora" : "Cancelar"}
            </Button>
          )}
          {j.state !== "finished" && (
            <Button size="sm" onClick={onEditar}>
              Editar
            </Button>
          )}
          <Button size="sm" variant="danger" onClick={onRemover}>
            Remover
          </Button>
        </span>
      </div>
    </li>
  );
}

/**
 * FormJanela cria e edita.
 *
 * Os horários são digitados no fuso do navegador, e é nele que a
 * recorrente repete o horário: "todo domingo às 2h" é às 2h de quem
 * declarou, mesmo depois da troca de horário de verão.
 *
 * O alvo é escolhido na árvore de monitores, e a conversão para grupos e
 * monitores fica em alvosDaSelecao: o grupo inteiro marcado vira o grupo
 * como alvo. Grupo sem monitor não aparece na árvore e, por ora, não se
 * escolhe aqui.
 */
function FormJanela({
  inicial,
  grupos,
  monitores,
  submit,
  onSave,
  onDone,
  onCancel,
}: {
  inicial?: MaintenanceWindow;
  grupos: Group[];
  monitores: Monitor[];
  submit: string;
  onSave: (input: MaintenanceInput) => Promise<unknown>;
  onDone: () => Promise<void>;
  onCancel: () => void;
}) {
  const padrao = useMemo(() => padraoAgora(), []);
  const [titulo, setTitulo] = useState(inicial?.title ?? "");
  const [mensagem, setMensagem] = useState(inicial?.message ?? "");
  const [inicio, setInicio] = useState(inicial ? paraCampo(inicial.starts_at) : padrao.inicio);
  const [fim, setFim] = useState(inicial ? paraCampo(inicial.ends_at) : padrao.fim);
  const [recorrencia, setRecorrencia] = useState<Recurrence>(inicial?.recurrence ?? "none");
  const arvore = useMemo(() => buildServiceTree(grupos, monitores), [grupos, monitores]);
  const [{ marcados: marcadosIniciais, preservados }] = useState(() =>
    selecaoDosAlvos(inicial ?? { group_ids: [], monitor_ids: [] }, arvore),
  );
  const [marcados, setMarcados] = useState(marcadosIniciais);
  const [enviando, setEnviando] = useState(false);
  const [campoErro, setCampoErro] = useState<{ campo: string; msg: string } | null>(null);
  const [erroGeral, setErroGeral] = useState<string | null>(null);

  // O fuso de uma janela já gravada é o dela; a nova nasce no do navegador.
  const fuso = inicial?.time_zone ?? fusoDoNavegador();
  async function salvar(e: FormEvent) {
    e.preventDefault();
    setCampoErro(null);
    setErroGeral(null);
    setEnviando(true);
    try {
      await onSave({
        title: titulo,
        message: mensagem,
        starts_at: doCampo(inicio),
        ends_at: doCampo(fim),
        recurrence: recorrencia,
        time_zone: fuso,
        ...alvosDaSelecao(arvore, marcados, preservados),
      });
      await onDone();
    } catch (err) {
      if (err instanceof ApiError && err.field) setCampoErro({ campo: err.field, msg: err.message });
      else setErroGeral(err instanceof ApiError ? err.message : "Não foi possível salvar a janela.");
    } finally {
      setEnviando(false);
    }
  }

  const erroDe = (campo: string) => (campoErro?.campo === campo ? campoErro.msg : undefined);
  const erroAlvo = erroDe("monitor_ids") ?? erroDe("group_ids");

  return (
    <form onSubmit={salvar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
      {erroGeral && <Alert>{erroGeral}</Alert>}

      <Field label="título" hint="Aparece na página pública, se algum alvo estiver publicado." error={erroDe("title")}>
        <Input
          value={titulo}
          onChange={(e) => setTitulo(e.target.value)}
          placeholder="deploy do RPA"
          autoFocus
          required
        />
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="início" error={erroDe("starts_at")}>
          <Input type="datetime-local" value={inicio} onChange={(e) => setInicio(e.target.value)} required />
        </Field>
        <Field label="fim" error={erroDe("ends_at")}>
          <Input type="datetime-local" value={fim} onChange={(e) => setFim(e.target.value)} required />
        </Field>
      </div>

      <Field
        label="repetir"
        hint={`Horários em ${fuso}. A recorrente repete o mesmo horário a partir do início.`}
        error={erroDe("recurrence") ?? erroDe("time_zone")}
      >
        <Select value={recorrencia} onChange={(e) => setRecorrencia(e.target.value as Recurrence)}>
          {(Object.keys(RECORRENCIA_LABEL) as Recurrence[]).map((r) => (
            <option key={r} value={r}>
              {RECORRENCIA_LABEL[r]}
            </option>
          ))}
        </Select>
      </Field>

      <fieldset className="flex flex-col gap-1.5">
        <legend className="eyebrow mb-1.5">em manutenção</legend>
        <MonitorTree
          groups={grupos}
          monitors={monitores}
          selected={marcados}
          onChange={setMarcados}
          note="Grupo inteiro marcado vale também para os monitores criados depois nele. Os dependentes de um monitor marcado ficam em manutenção junto."
        />
        {preservados.length > 0 && (
          <span className="text-small text-ink-3">
            Também em manutenção, sem monitores hoje:{" "}
            {preservados.map((id) => grupos.find((g) => g.id === id)?.path ?? `grupo ${id}`).join(", ")}
          </span>
        )}
        {erroAlvo && <span className="text-small text-down">{erroAlvo}</span>}
      </fieldset>

      <Field label="mensagem" hint="Opcional. O que a página pública diz junto do título." error={erroDe("message")}>
        <Textarea
          value={mensagem}
          onChange={(e) => setMensagem(e.target.value)}
          placeholder="A API fica indisponível por alguns minutos durante a atualização."
          className="min-h-[4rem]"
        />
      </Field>

      <FormActions submit={enviando ? "Salvando" : submit} busy={enviando} onCancel={onCancel} />
    </form>
  );
}
