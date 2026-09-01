import { useEffect, useState } from "react";
import { client, type Settings } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";

export function SettingsPage() {
  const { data, error, loading, reload } = useAsync(
    (signal) => client.getSettings({ signal }),
    [],
  );
  const [draft, setDraft] = useState<Settings | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [googleKey, setGoogleKey] = useState("");
  const { run, busy: saving, error: saveErr } = useAction();

  useEffect(() => {
    if (data) setDraft(structuredClone(data));
  }, [data]);

  if (loading) return <p className="text-sm text-slate-500">Loading…</p>;
  if (error) return <p className="text-sm text-red-600">{error}</p>;
  if (!draft || !data) return null;

  function save() {
    if (!draft) return;
    setMsg(null);
    const patch: Record<string, unknown> = {
      auto_match_threshold: draft.auto_match_threshold,
      audible: draft.audible,
      open_library: draft.open_library,
      google_books: {
        enabled: draft.google_books.enabled,
        ...(googleKey ? { api_key: googleKey } : {}),
      },
    };
    run(async () => {
      await client.patchSettings(patch);
      setGoogleKey("");
      setMsg("Saved.");
      reload();
    });
  }

  return (
    <div className="max-w-2xl space-y-8">
      <h1 className="text-xl font-semibold">Settings</h1>

      <section className="space-y-3">
        <h2 className="font-medium">Matching</h2>
        <label className="block text-sm">
          <span className="mb-1 block text-slate-500">
            Auto-match threshold: {draft.auto_match_threshold.toFixed(2)}
          </span>
          <input
            type="range"
            min={0.5}
            max={1}
            step={0.01}
            value={draft.auto_match_threshold}
            onChange={(e) =>
              setDraft({
                ...draft,
                auto_match_threshold: Number(e.target.value),
              })
            }
            className="w-full"
          />
        </label>
      </section>

      <section className="space-y-3">
        <h2 className="font-medium">Metadata providers</h2>

        <ProviderRow
          label="Audible"
          enabled={draft.audible.enabled}
          onToggle={(v) =>
            setDraft({ ...draft, audible: { ...draft.audible, enabled: v } })
          }
        >
          <select
            value={draft.audible.region}
            onChange={(e) =>
              setDraft({
                ...draft,
                audible: { ...draft.audible, region: e.target.value },
              })
            }
            className="rounded border border-slate-300 px-2 py-1 text-sm dark:border-slate-700 dark:bg-slate-800"
          >
            {["us", "uk", "ca", "au", "de", "fr", "it", "es", "jp", "in"].map(
              (r) => (
                <option key={r} value={r}>
                  {r.toUpperCase()}
                </option>
              ),
            )}
          </select>
        </ProviderRow>

        <ProviderRow
          label="Google Books"
          enabled={draft.google_books.enabled}
          onToggle={(v) =>
            setDraft({
              ...draft,
              google_books: { ...draft.google_books, enabled: v },
            })
          }
        >
          <input
            type="password"
            placeholder={
              draft.google_books.api_key_set
                ? "key set — leave blank to keep"
                : "API key (optional)"
            }
            value={googleKey}
            onChange={(e) => setGoogleKey(e.target.value)}
            className="rounded border border-slate-300 px-2 py-1 text-sm dark:border-slate-700 dark:bg-slate-800"
          />
        </ProviderRow>

        <ProviderRow
          label="Open Library"
          enabled={draft.open_library.enabled}
          onToggle={(v) => setDraft({ ...draft, open_library: { enabled: v } })}
        />
      </section>

      <section className="space-y-2">
        <h2 className="font-medium">Authentication</h2>
        {/* Fed from the server response, not `draft`: this form owns its own
            fields and must only resync when a real reload arrives, not every
            time an unrelated provider toggle mutates `draft`. */}
        <AuthSettingsForm current={data} onSaved={reload} />
      </section>

      <div className="flex items-center gap-3">
        <button
          onClick={save}
          disabled={saving}
          className="rounded bg-slate-900 px-4 py-2 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {saving ? "Saving…" : "Save providers & matching"}
        </button>
        {(saveErr ?? msg) && (
          <span className="text-sm text-slate-500">{saveErr ?? msg}</span>
        )}
      </div>
    </div>
  );
}

function AuthSettingsForm({
  current,
  onSaved,
}: {
  current: Settings;
  onSaved: () => void;
}) {
  const [enabled, setEnabled] = useState(current.auth.enabled);
  const [username, setUsername] = useState(current.auth.username ?? "");
  const [password, setPassword] = useState("");
  const [rotate, setRotate] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  // The server returns a newly minted API key exactly once, in the response to
  // the save that created it. Hold it here so the operator can copy it; there
  // is no way to read it back afterwards, only to rotate for a new one.
  const [newKey, setNewKey] = useState<string | null>(null);
  const { run, busy: saving, error: saveErr } = useAction();

  // `current` is the server response object; its identity changes only on a
  // real reload, so this resyncs after a save without clobbering in-progress
  // edits made while an unrelated setting was being changed. Password stays
  // blank by design.
  useEffect(() => {
    setEnabled(current.auth.enabled);
    setUsername(current.auth.username ?? "");
  }, [current]);

  function save() {
    setMsg(null);
    const auth: Record<string, unknown> = { enabled, rotate_api_key: rotate };
    if (username) auth.username = username;
    if (password) auth.password = password;
    run(async () => {
      const saved = await client.patchSettings({ auth });
      setPassword("");
      setRotate(false);
      setNewKey(saved.auth.api_key ?? null);
      setMsg("Saved. If you just enabled auth you may need to sign in.");
      onSaved();
    });
  }

  return (
    <div className="space-y-3 rounded border border-slate-200 p-3 dark:border-slate-800">
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          aria-label="Require login for the UI and API"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        Require login for the UI and API
      </label>
      <div className="grid gap-2 sm:grid-cols-2">
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Username</span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">
            Password {current.auth.enabled ? "(blank = keep)" : ""}
          </span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          />
        </label>
      </div>
      <label className="flex items-center gap-2 text-xs text-slate-500">
        <input
          type="checkbox"
          checked={rotate}
          onChange={(e) => setRotate(e.target.checked)}
        />
        Rotate API key on save{" "}
        {current.auth.api_key_set ? "(one is currently set)" : "(none set yet)"}
      </label>
      {newKey && (
        <div className="space-y-1 rounded border border-amber-300 bg-amber-50 p-2 dark:border-amber-700 dark:bg-amber-950">
          <p className="text-xs font-medium">
            New API key — copy it now, it is not shown again
          </p>
          <code className="block break-all rounded bg-white px-2 py-1 font-mono text-xs dark:bg-slate-900">
            {newKey}
          </code>
          <p className="text-xs text-slate-500">
            Send it as the <code>X-Api-Key</code> header. Rotate above to
            replace it.
          </p>
        </div>
      )}
      <div className="flex items-center gap-3">
        <button
          onClick={save}
          disabled={saving}
          className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
        >
          {saving ? "Saving…" : "Save authentication"}
        </button>
        {(saveErr ?? msg) && (
          <span className="text-xs text-slate-500">{saveErr ?? msg}</span>
        )}
      </div>
    </div>
  );
}

function ProviderRow({
  label,
  enabled,
  onToggle,
  children,
}: {
  label: string;
  enabled: boolean;
  onToggle: (v: boolean) => void;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex items-center justify-between rounded border border-slate-200 px-3 py-2 dark:border-slate-800">
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          aria-label={`Enable ${label}`}
          checked={enabled}
          onChange={(e) => onToggle(e.target.checked)}
        />
        {label}
      </label>
      {children}
    </div>
  );
}
