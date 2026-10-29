import { Button, Checkbox, Field, Input, Select, Textarea } from "../components/ui";
import {
  argumentosDe,
  assercaoVazia,
  COMANDOS_REDIS,
  exemploDeChave,
  OPERADORES,
  type Assercao,
  type EntradaProtocolo,
  type TipoProtocolo,
} from "../lib/protocol-config";

/**
 * Campos dos monitores de Redis, PostgreSQL e MySQL.
 *
 * Separados do formulário principal porque só fazem sentido para esses
 * tipos, e porque a regra da senha — em branco mantém a guardada — precisa
 * estar dita na tela, ao lado do campo.
 */

const SSLMODES = ["", "disable", "allow", "prefer", "require", "verify-ca", "verify-full"];
const MYSQL_TLS = ["", "false", "true", "skip-verify", "preferred"];

export function ProtocolFields({
  tipo,
  valor,
  onChange,
  erroDe,
}: {
  tipo: TipoProtocolo;
  valor: EntradaProtocolo;
  onChange: (v: EntradaProtocolo) => void;
  erroDe: (campo: string) => string | undefined;
}) {
  function set<K extends keyof EntradaProtocolo>(k: K, v: EntradaProtocolo[K]) {
    onChange({ ...valor, [k]: v });
  }

  const redis = tipo === "redis";

  return (
    <>
      <div className="grid grid-cols-2 gap-4">
        <Field
          label="usuário"
          hint={redis ? "de ACL; vazio usa o default" : "de preferência só leitura"}
          error={erroDe("usuario")}
        >
          <Input
            value={valor.usuario}
            onChange={(e) => set("usuario", e.target.value)}
            autoComplete="off"
            required={!redis}
          />
        </Field>

        <Field
          label="senha"
          hint={
            valor.senhaGuardada
              ? "há uma guardada; deixe em branco para mantê-la"
              : "nunca é mostrada depois de salva"
          }
          error={erroDe("senha")}
        >
          <Input
            type="password"
            value={valor.senha}
            onChange={(e) => set("senha", e.target.value)}
            autoComplete="new-password"
            placeholder={valor.senhaGuardada ? "••••••••" : ""}
          />
        </Field>
      </div>

      {valor.senhaGuardada && valor.senha === "" && (
        <Checkbox checked={valor.removerSenha} onChange={() => set("removerSenha", !valor.removerSenha)}>
          remover a senha guardada
        </Checkbox>
      )}

      {redis ? (
        <RedisFields valor={valor} set={set} erroDe={erroDe} />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-4">
            <Field label="banco" hint="vazio usa o padrão do usuário" error={erroDe("banco")}>
              <Input value={valor.banco} onChange={(e) => set("banco", e.target.value)} />
            </Field>

            <Field label={tipo === "postgres" ? "sslmode" : "tls"} hint="criptografia da conexão">
              <Select value={valor.tls} onChange={(e) => set("tls", e.target.value)}>
                {(tipo === "postgres" ? SSLMODES : MYSQL_TLS).map((m) => (
                  <option key={m} value={m}>
                    {m === "" ? (tipo === "postgres" ? "padrão (prefer)" : "padrão (false)") : m}
                  </option>
                ))}
              </Select>
            </Field>
          </div>

          <Field
            label="consulta"
            hint="Roda numa transação somente leitura; o tempo dela é a latência. Vazia usa SELECT 1."
            error={erroDe("consulta")}
          >
            <Textarea
              value={valor.consulta}
              onChange={(e) => set("consulta", e.target.value)}
              placeholder="SELECT 1"
              className="tabular min-h-[4rem]"
            />
          </Field>
        </>
      )}
    </>
  );
}

function RedisFields({
  valor,
  set,
  erroDe,
}: {
  valor: EntradaProtocolo;
  set: <K extends keyof EntradaProtocolo>(k: K, v: EntradaProtocolo[K]) => void;
  erroDe: (campo: string) => string | undefined;
}) {
  function mudar(i: number, parcial: Partial<Assercao>) {
    set(
      "assercoes",
      valor.assercoes.map((a, j) => (j === i ? { ...a, ...parcial } : a)),
    );
  }

  return (
    <>
      <div className="grid grid-cols-2 gap-4">
        <Field label="banco" hint="número; 0 é o padrão" error={erroDe("banco")}>
          <Input
            value={valor.banco}
            onChange={(e) => set("banco", e.target.value)}
            inputMode="numeric"
            className="tabular"
            placeholder="0"
          />
        </Field>

        <Field
          label="acusar memória acima de"
          hint="MB de used_memory; vazio desliga"
          error={erroDe("memoria")}
        >
          <Input
            value={valor.memoriaMaximaMB}
            onChange={(e) => set("memoriaMaximaMB", e.target.value)}
            inputMode="decimal"
            className="tabular"
          />
        </Field>
      </div>

      <div className="flex flex-wrap gap-x-4">
        <Checkbox checked={valor.redisTLS} onChange={() => set("redisTLS", !valor.redisTLS)}>
          conectar com TLS
        </Checkbox>
        {valor.redisTLS && (
          <Checkbox
            checked={valor.ignorarCertificado}
            onChange={() => set("ignorarCertificado", !valor.ignorarCertificado)}
          >
            não validar o certificado
          </Checkbox>
        )}
      </div>

      <Field
        label="condições"
        hint="Comandos de leitura conferidos depois do PING. Ex.: SCARD rq:workers:default >= 1 acusa quando não há worker do RQ; WORKERS bull:minha-fila >= 1, do Bull/BullMQ."
        error={erroDe("assercoes")}
      >
        <div className="flex flex-col gap-2">
          {valor.assercoes.map((a, i) => {
            const pede = argumentosDe(a.comando);
            return (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <Select
                  value={a.comando}
                  onChange={(e) => mudar(i, { comando: e.target.value })}
                  className="!w-28"
                  aria-label="comando"
                >
                  {COMANDOS_REDIS.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </Select>
                {pede.secao && (
                  <Input
                    value={a.secao}
                    onChange={(e) => mudar(i, { secao: e.target.value })}
                    placeholder="memory"
                    className="!w-28 tabular"
                    aria-label="seção"
                  />
                )}
                {pede.chave && (
                  <Input
                    value={a.chave}
                    onChange={(e) => mudar(i, { chave: e.target.value })}
                    placeholder={exemploDeChave(a.comando)}
                    className="min-w-0 flex-1 tabular"
                    aria-label="chave"
                  />
                )}
                {pede.campo && (
                  <Input
                    value={a.campo}
                    onChange={(e) => mudar(i, { campo: e.target.value })}
                    placeholder={a.comando === "INFO" ? "used_memory" : "campo"}
                    className="min-w-0 flex-1 tabular"
                    aria-label="campo"
                  />
                )}
                <Select
                  value={a.op}
                  onChange={(e) => mudar(i, { op: e.target.value })}
                  className="!w-16"
                  aria-label="operador"
                >
                  {OPERADORES.map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </Select>
                <Input
                  value={a.valor}
                  onChange={(e) => mudar(i, { valor: e.target.value })}
                  inputMode="decimal"
                  className="!w-20 tabular"
                  aria-label="valor esperado"
                />
                <Button
                  type="button"
                  onClick={() => set("assercoes", valor.assercoes.filter((_, j) => j !== i))}
                  aria-label="remover condição"
                >
                  ×
                </Button>
              </div>
            );
          })}
          <div>
            <Button type="button" onClick={() => set("assercoes", [...valor.assercoes, assercaoVazia()])}>
              + condição
            </Button>
          </div>
        </div>
      </Field>
    </>
  );
}
