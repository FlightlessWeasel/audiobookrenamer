import { client, type Job } from "../api/client";

const TERMINAL: Job["status"][] = ["done", "failed", "canceled"];
const DEFAULT_POLL_MS = 1000;

export function isTerminalJob(status: Job["status"]): boolean {
  return TERMINAL.includes(status);
}

// Polls GET /jobs/{id} until the job reaches a terminal status, then resolves
// with the final row. The first poll fires immediately, so a job that already
// finished returns without a delay. Rejects with an AbortError when `signal`
// aborts (e.g. the caller unmounted) and propagates any request failure.
//
// Callers that enqueue a job and then need to reflect its outcome — a state
// change the job makes server-side — use this instead of reloading straight
// after enqueue, when the job is still "queued" and nothing has changed yet.
export async function waitForJob(
  id: string,
  opts: { signal?: AbortSignal; pollMs?: number } = {},
): Promise<Job> {
  const pollMs = opts.pollMs ?? DEFAULT_POLL_MS;
  for (;;) {
    const job = await client.getJob(id, { signal: opts.signal });
    if (isTerminalJob(job.status)) return job;
    await delay(pollMs, opts.signal);
  }
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        reject(new DOMException("Aborted", "AbortError"));
      },
      { once: true },
    );
  });
}
