import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError, type TOTPSetup, type TOTPStatus } from "../api/types";
import { QrCode } from "../components/QrCode";
import { Alert, Button, Field, Input, Loading, TextLink } from "../components/ui";

/**
 * Verificação em duas etapas da própria conta.
 *
 * Três momentos: desligada (com o cadastro), ligada (com a troca dos
 * códigos e o desligamento) e o instante entre os dois em que os códigos
 * de recuperação aparecem pela única vez.
 *
 * onChanged avisa que o estado da conta mudou, para quem está em volta
 * reler /auth/me. Só depois de a pessoa dizer que guardou os códigos:
 * avisar antes faria a tela de cadastro obrigatório sumir com eles.
 */
export function TwoFactor({ onChanged }: { onChanged?: () => void }) {
  const [status, setStatus] = useState<TOTPStatus | null>(null);
  const [codigos, setCodigos] = useState<string[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      setStatus(await api.totpStatus());
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível carregar a verificação em duas etapas.");
    }
  }, []);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-lead font-medium">Verificação em duas etapas</h2>
        <p className="text-body text-ink-2">
          Além da senha, o login pede um código de seis dígitos do aplicativo autenticador do seu
          celular. Quem descobrir a senha sozinha não entra. Tokens de API não passam por aqui.
        </p>
      </div>

      {erro && <Alert>{erro}</Alert>}

      {codigos ? (
        <CodigosDeRecuperacao
          codigos={codigos}
          onDone={() => {
            setCodigos(null);
            void carregar();
            onChanged?.();
          }}
        />
      ) : status === null ? (
        <Loading what="verificação em duas etapas" />
      ) : status.enabled ? (
        <Ligada
          status={status}
          onCodes={setCodigos}
          onDisabled={() => {
            void carregar();
            onChanged?.();
          }}
        />
      ) : (
        <Cadastro onEnabled={setCodigos} />
      )}
    </section>
  );
}

/** Cadastro sorteia o segredo, mostra o QR code e confirma com um código. */
function Cadastro({ onEnabled }: { onEnabled: (codigos: string[]) => void }) {
  const [setup, setSetup] = useState<TOTPSetup | null>(null);
  const [codigo, setCodigo] = useState("");
  const [erro, setErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);

  async function comecar() {
    setErro(null);
    try {
      setSetup(await api.totpSetup());
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível começar o cadastro.");
    }
  }

  async function confirmar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    try {
      const { recovery_codes } = await api.totpEnable(codigo.trim());
      onEnabled(recovery_codes);
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível confirmar o código.");
      setCodigo("");
    } finally {
      setEnviando(false);
    }
  }

  if (!setup) {
    return (
      <div className="flex flex-col gap-3">
        {erro && <Alert>{erro}</Alert>}
        <p className="text-body">Desligada.</p>
        <Button variant="primary" onClick={comecar} className="self-start">
          Ligar verificação em duas etapas
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {erro && <Alert>{erro}</Alert>}

      <ol className="flex flex-col gap-4">
        <li className="flex flex-col gap-3">
          <p className="text-body">
            1. No aplicativo autenticador (Google Authenticator, Authy, 1Password, Bitwarden…),
            escaneie o código:
          </p>
          <QrCode value={setup.uri} label="QR code para cadastrar o UpWatch no aplicativo" />
          <div className="flex flex-col gap-1">
            <span className="text-small text-ink-3">Sem câmera? Digite esta chave no aplicativo:</span>
            <code className="block overflow-x-auto rounded-sm border border-line-strong bg-surface px-2.5 py-1.5 text-small tabular">
              {agrupar(setup.secret)}
            </code>
          </div>
        </li>

        <li>
          <form onSubmit={confirmar} className="flex flex-col gap-3">
            <Field label="2. código que o aplicativo mostra">
              <Input
                value={codigo}
                onChange={(e) => setCodigo(e.target.value)}
                autoComplete="one-time-code"
                inputMode="numeric"
                placeholder="000000"
                spellCheck={false}
                required
                className="tabular sm:max-w-[12rem]"
              />
            </Field>
            <Button type="submit" variant="primary" disabled={enviando} className="self-start">
              {enviando ? "Confirmando" : "Confirmar e ligar"}
            </Button>
          </form>
        </li>
      </ol>
    </div>
  );
}

/** agrupar quebra a chave em blocos de quatro, como os aplicativos mostram. */
function agrupar(chave: string): string {
  return chave.match(/.{1,4}/g)?.join(" ") ?? chave;
}

/**
 * CodigosDeRecuperacao mostra os códigos uma vez, com cópia e download.
 *
 * Mesmo aviso do token de API: fechar a tela sem guardar é perdê-los.
 */
function CodigosDeRecuperacao({ codigos, onDone }: { codigos: string[]; onDone: () => void }) {
  const [copiado, setCopiado] = useState(false);
  const texto = codigos.join("\n") + "\n";

  async function copiar() {
    try {
      await navigator.clipboard.writeText(texto);
      setCopiado(true);
    } catch {
      setCopiado(false);
    }
  }

  function baixar() {
    const url = URL.createObjectURL(new Blob([texto], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "upwatch-codigos-de-recuperacao.txt";
    a.click();
    URL.revokeObjectURL(url);
  }

  return (
    <div className="flex flex-col gap-3 border-l-2 border-degraded bg-degraded-dim px-3 py-2.5">
      <p className="text-body font-medium">Guarde estes códigos agora. Eles não aparecem de novo.</p>
      <p className="text-small text-ink-2">
        Cada um entra uma vez no lugar do código do aplicativo, para quando o celular não estiver à
        mão. Guarde fora do celular — num gerenciador de senhas ou impresso.
      </p>
      <ul
        aria-label="códigos de recuperação"
        className="grid grid-cols-2 gap-x-4 gap-y-1 rounded-sm border border-line-strong bg-surface px-2.5 py-2 text-small tabular"
      >
        {codigos.map((c) => (
          <li key={c}>
            <code>{c}</code>
          </li>
        ))}
      </ul>
      <span className="flex flex-wrap items-center gap-2">
        <Button size="sm" onClick={copiar}>
          {copiado ? "Copiados" : "Copiar"}
        </Button>
        <Button size="sm" onClick={baixar}>
          Baixar .txt
        </Button>
        <TextLink onClick={onDone} className="ml-2">
          já guardei
        </TextLink>
      </span>
    </div>
  );
}

type Acao = "codigos" | "desligar";

/** Ligada mostra o estado e as duas ações que pedem senha e código. */
function Ligada({
  status,
  onCodes,
  onDisabled,
}: {
  status: TOTPStatus;
  onCodes: (codigos: string[]) => void;
  onDisabled: () => void;
}) {
  const [acao, setAcao] = useState<Acao | null>(null);
  const [senha, setSenha] = useState("");
  const [codigo, setCodigo] = useState("");
  const [erro, setErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);

  function abrir(a: Acao | null) {
    setAcao(a);
    setSenha("");
    setCodigo("");
    setErro(null);
  }

  async function confirmar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    try {
      if (acao === "codigos") {
        const { recovery_codes } = await api.totpRecoveryCodes(senha, codigo.trim());
        abrir(null);
        onCodes(recovery_codes);
      } else {
        await api.totpDisable(senha, codigo.trim());
        abrir(null);
        onDisabled();
      }
    } catch (e) {
      setErro(e instanceof ApiError ? e.message : "Não foi possível concluir.");
    } finally {
      setEnviando(false);
    }
  }

  const poucos = status.recovery_codes_left <= 3;

  return (
    <div className="flex flex-col gap-4">
      <p className="text-body">
        Ligada.{" "}
        <span className={poucos ? "text-down" : "text-ink-2"}>
          {status.recovery_codes_left === 1
            ? "Resta 1 código de recuperação."
            : `Restam ${status.recovery_codes_left} códigos de recuperação.`}
        </span>
      </p>

      {acao === null ? (
        <span className="flex flex-wrap gap-2">
          <Button onClick={() => abrir("codigos")}>Gerar códigos novos</Button>
          <Button variant="danger" onClick={() => abrir("desligar")}>
            Desligar
          </Button>
        </span>
      ) : (
        <form onSubmit={confirmar} className="flex flex-col gap-4 rounded-sm border border-line-strong p-4">
          <p className="text-body text-ink-2">
            {acao === "codigos"
              ? "Os códigos atuais deixam de valer. Confirme com a senha e um código."
              : status.required
                ? "Esta instalação exige a verificação de administradores: desligada, você só volta a usar a interface depois de cadastrar de novo. Confirme com a senha e um código."
                : "O login volta a pedir só a senha, e as outras sessões abertas são encerradas. Confirme com a senha e um código."}
          </p>
          {erro && <Alert>{erro}</Alert>}
          <Field label="senha">
            <Input
              type="password"
              value={senha}
              onChange={(e) => setSenha(e.target.value)}
              autoComplete="current-password"
              required
            />
          </Field>
          <Field label="código" hint="Do aplicativo, ou um código de recuperação.">
            <Input
              value={codigo}
              onChange={(e) => setCodigo(e.target.value)}
              autoComplete="one-time-code"
              spellCheck={false}
              required
              className="tabular"
            />
          </Field>
          <span className="flex gap-2">
            <Button type="submit" variant={acao === "desligar" ? "danger" : "primary"} disabled={enviando}>
              {acao === "codigos" ? "Gerar códigos novos" : "Desligar"}
            </Button>
            <Button type="button" onClick={() => abrir(null)}>
              Cancelar
            </Button>
          </span>
        </form>
      )}
    </div>
  );
}
