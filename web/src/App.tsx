import { useCallback, useEffect, useState } from "react";
import { api, ENROLLMENT_REQUIRED } from "./api/client";
import { ApiError, type User } from "./api/types";
import { Gate, Mark } from "./screens/Gate";
import { GroupScreen } from "./screens/GroupScreen";
import { Home } from "./screens/Home";
import { MonitorDetail } from "./screens/MonitorDetail";
import { MonitorForm } from "./screens/MonitorForm";
import { Settings } from "./screens/Settings";
import { Status } from "./screens/Status";
import { TwoFactor } from "./screens/TwoFactor";
import { TextLink } from "./components/ui";
import { navigate, useRoute } from "./lib/router";

/** Estados possíveis da aplicação antes de mostrar qualquer tela. */
type Session =
  | { kind: "loading" }
  | { kind: "setup" }
  | { kind: "anonymous" }
  | { kind: "signed-in"; user: User };

export function App() {
  const [session, setSession] = useState<Session>({ kind: "loading" });
  const route = useRoute();

  const publica = route.name === "status";

  const resolve = useCallback(async () => {
    try {
      const user = await api.me();
      setSession({ kind: "signed-in", user });
    } catch (e) {
      if (e instanceof ApiError && e.unauthenticated) {
        // Instalação sem conta abre o assistente em vez do login: pedir
        // credencial que ainda não existe seria um beco sem saída.
        const { needs_setup } = await api.needsSetup();
        setSession({ kind: needs_setup ? "setup" : "anonymous" });
        return;
      }
      setSession({ kind: "anonymous" });
    }
  }, []);

  useEffect(() => {
    if (publica) return;
    void resolve();
  }, [resolve, publica]);

  useEffect(() => {
    const reler = () => void resolve();
    window.addEventListener(ENROLLMENT_REQUIRED, reler);
    return () => window.removeEventListener(ENROLLMENT_REQUIRED, reler);
  }, [resolve]);

  // A página pública é resolvida antes de qualquer verificação de sessão.
  //
  // Quem abre aquele link não tem conta: mostrar-lhe uma tela de login
  // anularia o propósito de existir um endereço para compartilhar. E nem
  // sequer consultamos /auth/me antes de desenhar — seria latência a mais
  // numa tela que precisa carregar rápido justamente quando o serviço
  // está com problema.
  if (route.name === "status") {
    return <Status slug={route.slug} />;
  }

  if (session.kind === "loading") {
    return (
      <div className="flex min-h-dvh items-center justify-center">
        <Mark />
      </div>
    );
  }

  if (session.kind === "setup" || session.kind === "anonymous") {
    return <Gate mode={session.kind === "setup" ? "setup" : "login"} onDone={resolve} />;
  }

  async function sair() {
    await api.logout();
    setSession({ kind: "anonymous" });
    navigate({ name: "board" });
  }

  // Administrador que a instalação obriga a cadastrar o segundo fator vê
  // só o cadastro: o servidor recusa o resto, e mostrar o painel para ele
  // falhar tela a tela seria pior que dizer logo o que falta.
  if (session.user.two_factor_enrollment_required) {
    return <CadastroObrigatorio onDone={resolve} onSignOut={sair} />;
  }

  return (
    <div className="min-h-dvh">
      <TopBar user={session.user} onSignOut={sair} />

      <main>
        {route.name === "board" && <Home podeEscrever={session.user.role === "admin"} />}
        {route.name === "group" && (
          <GroupScreen id={route.id} podeEscrever={session.user.role === "admin"} />
        )}
        {route.name === "monitor" && (
          <MonitorDetail
            id={route.id}
            tab={route.tab ?? "eventos"}
            anchor={route.anchor}
            eu={session.user}
          />
        )}
        {route.name === "monitor-new" && <MonitorForm />}
        {route.name === "monitor-edit" && <MonitorForm id={route.id} />}
        {(route.name === "ajustes" || route.name === "ajustes-pagina") && (
          <Settings
            eu={session.user}
            section={route.name === "ajustes" ? route.section : "paginas"}
            pageId={route.name === "ajustes-pagina" ? route.id : undefined}
            onSignedOut={() => setSession({ kind: "anonymous" })}
          />
        )}
      </main>
    </div>
  );
}

function CadastroObrigatorio({ onDone, onSignOut }: { onDone: () => void; onSignOut: () => void }) {
  return (
    <div className="mx-auto flex max-w-2xl flex-col gap-6 px-5 py-8">
      <div className="flex items-center justify-between">
        <Mark />
        <TextLink onClick={onSignOut}>sair</TextLink>
      </div>
      <p className="max-w-prose border-l-2 border-degraded pl-3 text-body text-ink-2">
        Esta instalação exige verificação em duas etapas de quem administra. Cadastre o aplicativo
        autenticador para continuar; até lá, o resto da interface fica fechado.
      </p>
      <TwoFactor onChanged={onDone} />
    </div>
  );
}

function TopBar({ user, onSignOut }: { user: User; onSignOut: () => void }) {
  return (
    // sticky: numa lista longa de alvos, rolar até o fim não deveria custar
    // a volta ao painel nem o acesso aos ajustes.
    <div className="sticky top-0 z-10 border-b border-line bg-paper/95 backdrop-blur-sm">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-5 py-2.5">
        {/* -m-1 p-1 dá área de clique à marca sem deslocá-la da margem: o
            alvo cresce para dentro do preenchimento, não para o lado. */}
        <button
          onClick={() => navigate({ name: "board" })}
          className="pressable -m-1 flex items-center rounded-sm p-1 opacity-100 hover:opacity-70 active:opacity-90"
          aria-label="Ir para o painel"
        >
          <Mark />
        </button>

        <div className="flex items-center gap-4">
          <span className="flex items-baseline gap-2">
            <span className="text-small text-ink-3">{user.username}</span>
            {/* O papel fica visível: sem isso, um observador só descobre
                que não pode mexer ao tentar e receber uma recusa. */}
            {user.role === "viewer" && <span className="eyebrow">só leitura</span>}
          </span>
          <TextLink onClick={() => navigate({ name: "ajustes" })}>ajustes</TextLink>
          <TextLink onClick={onSignOut}>sair</TextLink>
        </div>
      </div>
    </div>
  );
}
