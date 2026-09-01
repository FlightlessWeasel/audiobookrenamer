import { useEffect, useRef, useState } from "react";
import { client } from "../api/client";
import { useAction } from "../lib/useAction";

export function Login({ onSuccess }: { onSuccess: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const usernameRef = useRef<HTMLInputElement>(null);
  // On success `onSuccess()` unmounts this component; useAction drops the
  // trailing "not busy" update when that happens.
  const { run, busy, error: err } = useAction();

  // After a failed attempt, drop focus back on the username field so the user
  // can retry from the keyboard. The error itself is announced via role="alert".
  useEffect(() => {
    if (err) usernameRef.current?.focus();
  }, [err]);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    run(async () => {
      await client.login(username, password);
      onSuccess();
    });
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <form
        onSubmit={submit}
        className="w-full max-w-sm space-y-4 rounded-lg border border-slate-200 bg-white p-6 dark:border-slate-800 dark:bg-slate-900"
      >
        <h1 className="text-lg font-semibold">Audiobook Library Manager</h1>
        <label className="block text-sm">
          <span className="mb-1 block text-slate-500">Username</span>
          <input
            ref={usernameRef}
            autoFocus
            name="username"
            autoComplete="username"
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
        <label className="block text-sm">
          <span className="mb-1 block text-slate-500">Password</span>
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
        {err && (
          <p role="alert" className="text-sm text-red-600">
            {err}
          </p>
        )}
        <button
          type="submit"
          disabled={busy}
          className="w-full rounded bg-slate-900 px-3 py-2 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
