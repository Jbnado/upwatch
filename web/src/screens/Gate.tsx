import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError } from "../api/types";
import { Alert, Button, Field, Input, TextLink } from "../components/ui";

/**
 * Tela de entrada, em dois modos.
 *
 * Primeiro acesso e login compartilham o mesmo layout porque são o mesmo
 * momento para quem chega: provar quem é antes de ver qualquer coisa.
 */

type GateProps = {
  mode: "setup" | "login";
  onDone: () => void;
};

export function Gate({ mode, onDone }: GateProps) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const [erro, setErro] = useState<string | null>(null);
  const [campoErro, setCampoErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);
  // Desafio do segundo passo: a senha conferiu, falta o código.
  const [desafio, setDesafio] = useState<string | null>(null);

  const criando = mode === "setup";

  async function enviar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    setCampoErro(null);

    try {
      if (criando) {
        // Os espaços em volta vêm da cópia do log, não do token.
        await api.createAdmin(username, password, setupToken.trim());
      }
      const res = await api.login(username, password);
      if (res.status === "two_factor_required") {
        setDesafio(res.challenge);
        return;
      }
      onDone();
    } catch (e) {
      if (e instanceof ApiError) {
        setErro(e.message);
        setCampoErro(e.field ?? null);
      } else {
        setErro("Não foi possível concluir. Verifique se o servidor está no ar.");
      }
    } finally {
      setEnviando(false);
    }
  }

  if (desafio) {
    return (
      <SegundoPasso
        desafio={desafio}
        onDone={onDone}
        onExpired={(motivo) => {
          // O desafio morreu (venceu, esgotou): sem ele o código não
          // serve, e a única saída é a senha de novo.
          setDesafio(null);
          setPassword("");
          setErro(motivo);
        }}
      />
    );
  }

  return (
    <div className="flex min-h-dvh items-center justify-center px-5">
      <div className="flex w-full max-w-[340px] flex-col gap-6">
        <Mark />

        <div className="flex flex-col gap-1.5">
          <h1 className="text-title font-semibold tracking-tight">
            {criando ? "Crie a conta de administração" : "Entrar"}
          </h1>
          <p className="text-body text-ink-2">
            {criando
              ? "Esta é a única conta criada sem login, e por isso pede o token de configuração, que só quem tem acesso ao servidor vê. Depois dela, o cadastro fecha."
              : "Use as credenciais definidas na instalação."}
          </p>
        </div>

        {erro && <Alert>{erro}</Alert>}

        <form onSubmit={enviar} className="flex flex-col gap-4">
          <Field label="usuário" error={campoErro === "username" ? erro ?? undefined : undefined}>
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
              required
            />
          </Field>

          <Field
            label="senha"
            hint={criando ? "No mínimo 12 caracteres." : undefined}
            error={campoErro === "password" ? erro ?? undefined : undefined}
          >
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete={criando ? "new-password" : "current-password"}
              required
            />
          </Field>

          {criando && (
            <Field
              label="token de configuração"
              hint="Está no log do servidor, na linha com token_de_configuracao (por exemplo, docker compose logs upwatch). Se você definiu UPWATCH_SETUP_TOKEN, é o valor dela."
              error={campoErro === "setup_token" ? erro ?? undefined : undefined}
            >
              <Input
                value={setupToken}
                onChange={(e) => setSetupToken(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                required
              />
            </Field>
          )}

          <Button type="submit" variant="primary" disabled={enviando} className="mt-2">
            {enviando ? "Aguarde" : criando ? "Criar conta e entrar" : "Entrar"}
          </Button>
        </form>
      </div>
    </div>
  );
}

/**
 * SegundoPasso pede o código do aplicativo ou, na falta do celular, um
 * código de recuperação.
 *
 * O campo é um só, e o servidor reconhece qual dos dois chegou. A troca
 * de modo muda o texto e o teclado: seis dígitos abrem o teclado
 * numérico no celular, e o código de recuperação tem letras.
 */
function SegundoPasso({
  desafio,
  onDone,
  onExpired,
}: {
  desafio: string;
  onDone: () => void;
  onExpired: (motivo: string) => void;
}) {
  const [codigo, setCodigo] = useState("");
  const [recuperacao, setRecuperacao] = useState(false);
  const [erro, setErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);

  async function verificar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);

    try {
      await api.loginSecondFactor(desafio, codigo.trim());
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.field === "challenge") {
        onExpired(e.message);
      } else if (e instanceof ApiError) {
        setErro(e.message);
        setCodigo("");
      } else {
        setErro("Não foi possível concluir. Verifique se o servidor está no ar.");
      }
    } finally {
      setEnviando(false);
    }
  }

  return (
    <div className="flex min-h-dvh items-center justify-center px-5">
      <div className="flex w-full max-w-[340px] flex-col gap-6">
        <Mark />

        <div className="flex flex-col gap-1.5">
          <h1 className="text-title font-semibold tracking-tight">Verificação em duas etapas</h1>
          <p className="text-body text-ink-2">
            {recuperacao
              ? "Digite um dos códigos de recuperação que você guardou. Cada um vale uma vez."
              : "Digite o código de seis dígitos que o aplicativo autenticador mostra agora."}
          </p>
        </div>

        {erro && <Alert>{erro}</Alert>}

        <form onSubmit={verificar} className="flex flex-col gap-4">
          <Field label={recuperacao ? "código de recuperação" : "código"}>
            <Input
              // key troca o campo inteiro com o modo, para o celular
              // reabrir o teclado certo.
              key={recuperacao ? "recuperacao" : "totp"}
              value={codigo}
              onChange={(e) => setCodigo(e.target.value)}
              autoComplete="one-time-code"
              inputMode={recuperacao ? "text" : "numeric"}
              placeholder={recuperacao ? "xxxx-xxxx-xxxx-xxxx" : "000000"}
              spellCheck={false}
              autoFocus
              required
              className="tabular"
            />
          </Field>

          <Button type="submit" variant="primary" disabled={enviando} className="mt-2">
            {enviando ? "Aguarde" : "Verificar"}
          </Button>
        </form>

        <TextLink
          onClick={() => {
            setRecuperacao(!recuperacao);
            setCodigo("");
            setErro(null);
          }}
          className="self-start"
        >
          {recuperacao ? "usar o código do aplicativo" : "usar código de recuperação"}
        </TextLink>
      </div>
    </div>
  );
}

/**
 * A marca é a própria faixa de pulso em miniatura.
 *
 * O produto inteiro se resume a esse traço, então ele serve de logotipo
 * sem precisar de um símbolo inventado por fora.
 */
export function Mark({ className = "" }: { className?: string }) {
  const alturas = [40, 62, 34, 100, 55, 28, 46];

  return (
    <div className={`flex items-end gap-[3px] ${className}`} aria-label="UpWatch" role="img">
      {alturas.map((h, i) => (
        <span
          key={i}
          className={`w-[4px] rounded-xs ${i === 3 ? "bg-degraded" : "bg-up"}`}
          style={{ height: `${(h / 100) * 26}px` }}
        />
      ))}
    </div>
  );
}
