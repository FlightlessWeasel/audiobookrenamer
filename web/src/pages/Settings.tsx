import { useEffect, useRef, useState } from "react";
import { client, type Job, type Settings, type UpdateStatus } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import { waitForJob } from "../lib/waitForJob";

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

      <section className="space-y-3">
        <h2 className="font-medium">Updates</h2>
        <UpdatesSection />
      </section>
    </div>
  );
}

// Phases of the "Update & restart" flow. `installing` covers enqueue +
// following the self-update job; `restarting` polls /healthz until the server
// comes back on the new build; `timeout` is when it hasn't after ~60s.
type ApplyPhase =
  | { kind: "idle" }
  | { kind: "installing" }
  | { kind: "restarting"; latest: string; current: string }
  | { kind: "timeout" };

const HEALTH_POLL_MS = 2000;
const HEALTH_POLL_TRIES = 30; // ~60s

function UpdatesSection() {
  // Runs on mount so `current` is shown straight away; the "Check for updates"
  // button just reloads it.
  const check = useAsync((signal) => client.getUpdate({ signal }), []);
  const { run, isBusy, error: applyErr, mounted } = useAction();
  const [apply, setApply] = useState<ApplyPhase>({ kind: "idle" });
  // Aborts an in-flight waitForJob poll loop if the section unmounts.
  const jobAbort = useRef<AbortController | null>(null);
  useEffect(() => () => jobAbort.current?.abort(), []);

  // While `restarting`, poll /healthz until the version changes off `current`
  // (the server re-execed onto the new build), then hard-reload the SPA so it
  // picks up any new assets. Give up after HEALTH_POLL_TRIES.
  useEffect(() => {
    if (apply.kind !== "restarting") return;
    const { latest, current } = apply;
    let cancelled = false;
    let tries = 0;
    let timer: ReturnType<typeof setTimeout>;

    const tick = async () => {
      tries += 1;
      try {
        const h = await client.health();
        if (!cancelled && h.version && (h.version === latest || h.version !== current)) {
          window.location.reload();
          return;
        }
      } catch {
        // Expected: the HTTP server is down for a moment while it re-execs.
        // Keep polling until it answers again or we run out of tries.
      }
      if (cancelled) return;
      if (tries >= HEALTH_POLL_TRIES) {
        setApply({ kind: "timeout" });
        return;
      }
      timer = setTimeout(tick, HEALTH_POLL_MS);
    };

    timer = setTimeout(tick, HEALTH_POLL_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [apply]);

  function onApply(s: UpdateStatus) {
    if (
      !confirm(
        `Update to ${s.latest} and restart now?\n\n` +
          `The server will briefly go offline while it installs the new build and re-launches.`,
      )
    ) {
      return;
    }
    setApply({ kind: "installing" });
    jobAbort.current?.abort();
    jobAbort.current = new AbortController();
    run(async () => {
      const job = await client.applyUpdate();
      let done: Job | undefined;
      try {
        done = await waitForJob(job.id, { signal: jobAbort.current?.signal });
      } catch (e) {
        // An abort means the section unmounted — nothing to do.
        if (e instanceof DOMException && e.name === "AbortError") return;
        // Any other failure here is almost certainly the server going down as
        // it re-execs into the new build: the job persists its terminal row
        // before the restart, so a dropped poll is expected, not an error.
        // Fall through to the restart wait, which polls /healthz until the new
        // version answers.
      }
      if (!mounted.current) return;
      if (done && done.status !== "done") {
        setApply({ kind: "idle" });
        throw new Error(done.error || `update job ${done.status}`);
      }
      setApply({ kind: "restarting", latest: s.latest, current: s.current });
    }, "apply");
  }

  const s = check.data;
  const busy =
    check.loading || apply.kind === "installing" || apply.kind === "restarting";

  return (
    <div className="space-y-3 rounded border border-slate-200 p-3 dark:border-slate-800">
      <div className="flex items-center gap-3">
        <button
          onClick={() => check.reload()}
          disabled={busy}
          className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
        >
          {check.loading ? "Checking…" : "Check for updates"}
        </button>
        <span className="text-sm text-slate-500">
          Current version:{" "}
          <span className="font-mono">{s?.current || "unknown"}</span>
        </span>
      </div>

      {check.error && <p className="text-sm text-red-600">{check.error}</p>}
      {applyErr && <p className="text-sm text-red-600">{applyErr}</p>}

      {apply.kind === "installing" && (
        <p className="text-sm text-slate-500">
          Installing update… the server will restart when this finishes.
        </p>
      )}
      {apply.kind === "restarting" && (
        <p className="text-sm text-slate-500">
          Restarting on {apply.latest}… waiting for the server to come back.
        </p>
      )}
      {apply.kind === "timeout" && (
        <div className="space-y-2 text-sm">
          <p className="text-amber-700 dark:text-amber-400">
            The server is taking longer than expected to come back — reload
            manually.
          </p>
          <button
            onClick={() => window.location.reload()}
            className="rounded border border-slate-300 px-3 py-1.5 disabled:opacity-50 dark:border-slate-700"
          >
            Reload
          </button>
        </div>
      )}

      {s && apply.kind === "idle" && (
        <div className="space-y-2 text-sm">
          {s.has_update ? (
            <div className="space-y-2 rounded border border-slate-200 p-3 dark:border-slate-800">
              <p className="font-medium">
                New version available:{" "}
                <span className="font-mono">{s.latest}</span>
              </p>
              {s.notes && (
                <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded bg-slate-50 p-2 text-xs dark:bg-slate-900">
                  {s.notes}
                </pre>
              )}
              {/^https:\/\//i.test(s.url) && (
                <a
                  href={s.url}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-block text-sky-600 hover:underline dark:text-sky-400"
                >
                  View release on GitHub
                </a>
              )}
              <div>
                {s.can_apply ? (
                  <button
                    onClick={() => onApply(s)}
                    disabled={isBusy("apply")}
                    className="rounded bg-slate-900 px-4 py-2 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
                  >
                    Update &amp; restart
                  </button>
                ) : (
                  <p className="text-amber-700 dark:text-amber-400">{s.reason}</p>
                )}
              </div>
            </div>
          ) : s.reason ? (
            <p className="rounded bg-amber-50 px-3 py-2 text-amber-800 dark:bg-amber-950 dark:text-amber-200">
              {s.reason}
            </p>
          ) : (
            <p className="text-slate-500">You're on the latest version.</p>
          )}
          {s.checked_at && (
            <p className="text-xs text-slate-400">
              Checked {new Date(s.checked_at).toLocaleString()}
            </p>
          )}
        </div>
      )}
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
