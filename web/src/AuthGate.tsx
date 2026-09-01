import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { client, type AuthStatus } from "./api/client";
import { Login } from "./pages/Login";

interface AuthCtx {
  status: AuthStatus;
  refresh: () => void;
}

const Ctx = createContext<AuthCtx>({
  status: { enabled: false, authenticated: true },
  refresh: () => {},
});

export const useAuth = () => useContext(Ctx);

export function AuthGate({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const inflight = useRef<AbortController | null>(null);

  const refresh = useCallback(() => {
    inflight.current?.abort();
    const ctrl = new AbortController();
    inflight.current = ctrl;
    client
      .authStatus({ signal: ctrl.signal })
      .then((s) => {
        if (!ctrl.signal.aborted) setStatus(s);
      })
      // Fail closed: if we can't confirm the auth state, assume auth is on and
      // the user is not signed in, so the UI shows the login screen rather
      // than exposing the app.
      .catch(() => {
        if (!ctrl.signal.aborted) setStatus({ enabled: true, authenticated: false });
      });
  }, []);

  useEffect(() => {
    refresh();
    const onUnauthorized = () => {
      // Cancel any in-flight status check so its (possibly stale, possibly
      // "authenticated") result can't land after this and re-open the app.
      inflight.current?.abort();
      setStatus({ enabled: true, authenticated: false });
    };
    window.addEventListener("abr:unauthorized", onUnauthorized);
    return () => {
      window.removeEventListener("abr:unauthorized", onUnauthorized);
      inflight.current?.abort();
    };
  }, [refresh]);

  if (!status) return null;
  if (status.enabled && !status.authenticated) {
    return <Login onSuccess={refresh} />;
  }
  return <Ctx.Provider value={{ status, refresh }}>{children}</Ctx.Provider>;
}
