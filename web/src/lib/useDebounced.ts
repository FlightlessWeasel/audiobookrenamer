import { useEffect, useState } from "react";

/**
 * Returns `value` delayed by `delayMs`. Every change to `value` resets the
 * timer, so a rapidly-changing input (e.g. a search box) only propagates once
 * it has settled for `delayMs`. The pending timer is cleared on unmount and on
 * each change, so a stale value can never land after a newer one.
 */
export function useDebounced<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(t);
  }, [value, delayMs]);

  return debounced;
}
