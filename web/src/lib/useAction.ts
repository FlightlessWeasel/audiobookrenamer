import { useCallback, useRef, useState } from "react";
import { errorMessage } from "./errorMessage";
import { useMountedRef } from "./useMountedRef";

export interface UseAction {
  /**
   * Run an async side-effecting action (a mutation, not a fetch — use `useAsync`
   * for those). `run` flips the matching busy flag on, awaits `fn`, captures any
   * thrown error into `error`, and clears the busy flag when `fn` settles —
   * skipping the trailing state update if the component has since unmounted.
   *
   * `key` scopes an independent busy flag so several unrelated actions (one per
   * row, say) can be in flight at once without sharing a spinner; omit it when a
   * component only ever runs one action at a time. A second `run` for a key that
   * is already in flight is ignored, which doubles as a synchronous re-entrancy
   * guard against a double-click landing before React re-renders the disabled
   * button.
   */
  run: (fn: () => Promise<unknown>, key?: string) => void;
  /** True while any action started through this hook is in flight. */
  busy: boolean;
  /** True while the action for `key` (default: the unkeyed action) is in flight. */
  isBusy: (key?: string) => boolean;
  /** Every in-flight key. Handy for `pending.size`-style checks. */
  pending: ReadonlySet<string>;
  /** Message of the last action that threw, until the next `run` or `clearError`. */
  error: string | null;
  clearError: () => void;
  /**
   * The hook's own mounted ref (it needs one internally anyway). Guard any
   * post-`await` `setState` inside an action `fn` with it, the same way you
   * would with a standalone `useMountedRef`.
   */
  mounted: ReturnType<typeof useMountedRef>;
}

const DEFAULT_KEY = "";

export function useAction(): UseAction {
  const mounted = useMountedRef();
  const [pending, setPending] = useState<ReadonlySet<string>>(() => new Set());
  const [error, setError] = useState<string | null>(null);
  // Synchronous in-flight guard: the `pending` state isn't observable until
  // React re-renders, so two dispatches in the same tick would both pass a
  // check against it and fire concurrent requests.
  const inflight = useRef<Set<string>>(new Set());

  const run = useCallback(
    (fn: () => Promise<unknown>, key: string = DEFAULT_KEY) => {
      if (inflight.current.has(key)) return;
      inflight.current.add(key);
      setPending((prev) => new Set(prev).add(key));
      setError(null);
      Promise.resolve()
        .then(fn)
        .catch((e) => {
          if (mounted.current) setError(errorMessage(e));
        })
        .finally(() => {
          inflight.current.delete(key);
          if (!mounted.current) return;
          setPending((prev) => {
            const next = new Set(prev);
            next.delete(key);
            return next;
          });
        });
    },
    [mounted],
  );

  const isBusy = useCallback(
    (key: string = DEFAULT_KEY) => pending.has(key),
    [pending],
  );
  const clearError = useCallback(() => setError(null), []);

  return {
    run,
    busy: pending.size > 0,
    isBusy,
    pending,
    error,
    clearError,
    mounted,
  };
}
