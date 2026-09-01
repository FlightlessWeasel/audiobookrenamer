import { useEffect, useState } from "react";
import { client, type Job } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import { statusBadgeClass, statusLabel } from "../lib/status";

// Activity shows job history and subscribes to the SSE stream for live updates.
interface JobEvent {
  job_id: string;
  type: string;
  total?: number;
  done?: number;
  message?: string;
  error?: string;
}

function parseJobEvent(raw: string): JobEvent | null {
  try {
    const v = JSON.parse(raw) as unknown;
    if (v && typeof v === "object" && typeof (v as JobEvent).job_id === "string" && typeof (v as JobEvent).type === "string") {
      return v as JobEvent;
    }
  } catch {
    // ignore malformed frame
  }
  return null;
}

const TERMINAL = ["done", "failed", "canceled"];
const MAX_LIVE = 200;

export function ActivityPage() {
  const { data, error, loading, reload } = useAsync((signal) => client.listJobs(100, { signal }), []);
  // Map (not a plain object) so eviction can rely on real insertion order:
  // V8 reorders integer-like string keys on a plain object, which would make
  // "drop the oldest" drop an arbitrary entry.
  const [live, setLive] = useState<Map<string, Partial<Job>>>(() => new Map());
  const [streamDown, setStreamDown] = useState(false);
  // Per-job busy flags: cancelling job A must not blank job B's pending Undo.
  const { run, isBusy, error: actionErr } = useAction();

  useEffect(() => {
    const es = new EventSource("/api/jobs/stream");
    es.addEventListener("job", (e) => {
      const ev = parseJobEvent((e as MessageEvent).data);
      if (!ev) return;
      setLive((prev) => {
        const next = new Map(prev);
        const cur = next.get(ev.job_id);
        next.set(ev.job_id, {
          ...cur,
          total: ev.total ?? cur?.total,
          done: ev.done ?? cur?.done,
          message: ev.message ?? cur?.message,
          error: ev.error ?? cur?.error,
          status:
            ev.type === "progress" || ev.type === "running"
              ? "running"
              : (ev.type as Job["status"]),
        });
        // Once a job reaches a terminal state the reloaded `data` carries the
        // authoritative row, so drop the live overlay.
        if (TERMINAL.includes(ev.type)) next.delete(ev.job_id);
        // Hard cap: evict the genuine oldest entries so a stream of events for
        // job ids that never terminate (or never appear in `data`) can't grow
        // the map unbounded. Re-`set`ting an existing key does not change its
        // position, so `keys().next()` is always the first-inserted id.
        while (next.size > MAX_LIVE) {
          const oldest = next.keys().next().value as string | undefined;
          if (oldest === undefined) break;
          next.delete(oldest);
        }
        return next;
      });
      if (TERMINAL.includes(ev.type)) reload();
    });
    // EventSource resends its last seen id as Last-Event-ID on reconnect, so
    // the happy path recovers missed events on its own. These cover the rest:
    // `reconcile` means the server's replay window was already evicted, and any
    // `open` (first connect or reconnect) re-syncs against authoritative state.
    // A redundant reload on the initial open is harmless.
    es.addEventListener("reconcile", () => reload());
    es.addEventListener("open", () => {
      setStreamDown(false);
      reload();
    });
    // EventSource reconnects on its own; surface the gap so the user knows the
    // rows aren't updating live. Cleared by the next "open".
    es.onerror = () => setStreamDown(true);
    return () => es.close();
  }, [reload]);

  function runAction(jobId: string, fn: () => Promise<unknown>) {
    run(async () => {
      await fn();
      reload();
    }, jobId);
  }

  if (loading) return <p className="text-sm text-slate-500">Loading…</p>;
  if (error) return <p className="text-sm text-red-600">{error}</p>;

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Activity</h1>
      {streamDown && (
        <p className="rounded bg-amber-100 px-3 py-2 text-sm text-amber-800">
          Live updates disconnected, retrying…
        </p>
      )}
      {actionErr && <p className="text-sm text-red-600">{actionErr}</p>}
      <div className="space-y-2">
        {data?.map((j) => {
          const merged = { ...j, ...live.get(j.id) };
          return (
            <div
              key={j.id}
              className="rounded border border-slate-200 bg-white p-3 text-sm dark:border-slate-800 dark:bg-slate-900"
            >
              <div className="flex items-center justify-between">
                <span className="font-medium capitalize">{merged.type}</span>
                <StatusBadge status={merged.status} />
              </div>
              <div className="mt-1 text-xs text-slate-500">
                {new Date(j.created_at).toLocaleString()}
                {merged.total ? ` · ${merged.done ?? 0}/${merged.total}` : ""}
                {merged.message ? ` · ${merged.message}` : ""}
              </div>
              {merged.error && <div className="mt-1 text-xs text-red-600">{merged.error}</div>}
              {merged.status === "running" && (
                <button
                  onClick={() => runAction(j.id, () => client.cancelJob(j.id))}
                  disabled={isBusy(j.id)}
                  className="mt-2 rounded border border-slate-300 px-2 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
                >
                  {isBusy(j.id) ? "Cancelling…" : "Cancel"}
                </button>
              )}
              {j.type === "organize" && merged.status === "done" && (
                <button
                  onClick={() => runAction(j.id, () => client.undoJob(j.id))}
                  disabled={isBusy(j.id)}
                  className="mt-2 rounded border border-slate-300 px-2 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
                >
                  {isBusy(j.id) ? "Undoing…" : "Undo"}
                </button>
              )}
            </div>
          );
        })}
        {data?.length === 0 && <p className="text-sm text-slate-500">No jobs yet.</p>}
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: Job["status"] }) {
  return (
    <span className={`rounded px-2 py-0.5 text-xs font-medium ${statusBadgeClass(status)}`}>
      {statusLabel(status)}
    </span>
  );
}
