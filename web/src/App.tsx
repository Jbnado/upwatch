import { useCallback, useEffect, useState } from "react";
import { api } from "./api/client";
import { ApiError, type User } from "./api/types";
import { Board } from "./screens/Board";
import { Gate, Mark } from "./screens/Gate";
import { MonitorDetail } from "./screens/MonitorDetail";
import { MonitorForm } from "./screens/MonitorForm";
import { Settings } from "./screens/Settings";
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
    void resolve();
  }, [resolve]);

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

  return (
    <div className="min-h-dvh">
      <TopBar user={session.user} onSignOut={sair} />

      <main>
        {route.name === "board" && <Board />}
        {route.name === "monitor" && <MonitorDetail id={route.id} />}
        {route.name === "monitor-new" && <MonitorForm />}
        {route.name === "monitor-edit" && <MonitorForm id={route.id} />}
        {route.name === "settings" && (
          <Settings onSignedOut={() => setSession({ kind: "anonymous" })} />
        )}
      </main>
    </div>
  );
}

function TopBar({ user, onSignOut }: { user: User; onSignOut: () => void }) {
  return (
    <div className="border-b border-line">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-5 py-2.5">
        <button
          onClick={() => navigate({ name: "board" })}
          className="flex items-center gap-2"
          aria-label="Ir para o painel"
        >
          <Mark />
        </button>

        <div className="flex items-center gap-4">
          <span className="text-[12px] text-ink-3">{user.username}</span>
          <button
            onClick={() => navigate({ name: "settings" })}
            className="eyebrow hover:text-ink"
          >
            ajustes
          </button>
          <button onClick={onSignOut} className="eyebrow hover:text-ink">
            sair
          </button>
        </div>
      </div>
    </div>
  );
}
