import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "./errorMessage";

interface AsyncState<T> {
  data: T | undefined;
  error: string | undefined;
  loading: boolean;
  reload: () => void;
}

function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}

// Minimal data-fetching hook. Re-runs when any dep changes or reload() is
// called. The in-flight request is aborted when deps change or the component
// unmounts, so a slow response can't land on a stale render or waste bandwidth.
// `fn` receives an AbortSignal it should forward to fetch/client calls.
export function useAsync<T>(
  fn: (signal: AbortSignal) => Promise<T>,
  deps: unknown[] = [],
): AsyncState<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [tick, setTick] = useState(0);

  // Callers pass an inline closure whose identity changes every render, so `fn`
  // can't sit in the effect's dependency array without re-firing the request on
  // every render. Keep the latest `fn` in a ref (refreshed during render, so
  // it's current for the effect that runs immediately after) and let `deps` be
  // the sole intended re-run trigger. Because the effect now lists `deps`
  // directly, react-hooks/exhaustive-deps still checks the caller's array — a
  // caller that closes over an unlisted value gets a lint warning on `deps`.
  const fnRef = useRef(fn);
  fnRef.current = fn;

  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setError(undefined);
    fnRef
      .current(ctrl.signal)
      .then((d) => {
        if (!ctrl.signal.aborted) setData(d);
      })
      .catch((e) => {
        if (ctrl.signal.aborted || isAbort(e)) return;
        setError(errorMessage(e));
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [...deps, tick]);

  return { data, error, loading, reload };
}
