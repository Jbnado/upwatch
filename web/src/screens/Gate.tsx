import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ApiError } from "../api/types";
import { Alert, Button, Field, Input } from "../components/ui";

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
  const [erro, setErro] = useState<string | null>(null);
  const [campoErro, setCampoErro] = useState<string | null>(null);
  const [enviando, setEnviando] = useState(false);

  const criando = mode === "setup";

  async function enviar(e: FormEvent) {
    e.preventDefault();
    setEnviando(true);
    setErro(null);
    setCampoErro(null);

    try {
      if (criando) {
        await api.createAdmin(username, password);
        await api.login(username, password);
      } else {
        await api.login(username, password);
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
              ? "Esta é a única conta criada sem autenticação. Depois dela, o cadastro fecha."
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

          <Button type="submit" variant="primary" disabled={enviando} className="mt-2">
            {enviando ? "Aguarde" : criando ? "Criar conta e entrar" : "Entrar"}
          </Button>
        </form>
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
