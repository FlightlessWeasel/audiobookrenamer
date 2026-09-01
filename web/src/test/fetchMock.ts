import { vi } from "vitest";

export interface MockResponse {
  status?: number;
  /** Raw body string. If an object is passed it is JSON-stringified. */
  body?: string | object;
  statusText?: string;
}

/**
 * Replaces global.fetch with a stub that resolves each call from `handler`.
 * `handler` receives the requested path (without the leading `/api`) and the
 * RequestInit, and returns a MockResponse (or a Response directly). If the
 * RequestInit carries an AbortSignal, the returned promise rejects with an
 * AbortError as soon as that signal aborts — matching real fetch semantics so
 * abort-based cancellation can be tested.
 */
export function mockFetch(
  handler: (path: string, init: RequestInit | undefined) => MockResponse | Response | Promise<MockResponse | Response>,
) {
  const fn = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input.toString();
    const path = url.replace(/^\/api/, "");
    const signal = init?.signal ?? undefined;

    const work = (async () => {
      const r = await handler(path, init);
      if (r instanceof Response) return r;
      const bodyStr = typeof r.body === "string" || r.body === undefined ? r.body : JSON.stringify(r.body);
      return new Response(bodyStr, {
        status: r.status ?? 200,
        statusText: r.statusText ?? "",
        headers: { "Content-Type": "application/json" },
      });
    })();

    if (!signal) return work;
    if (signal.aborted) return Promise.reject(new DOMException("Aborted", "AbortError"));
    return new Promise<Response>((resolve, reject) => {
      signal.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      work.then(resolve, reject);
    });
  });
  vi.stubGlobal("fetch", fn);
  return fn;
}

/** A deferred promise, handy for holding a mock response open. */
export function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}
