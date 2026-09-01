import { useEffect, useRef } from "react";

/**
 * Returns a ref whose `.current` is `true` while the component is mounted and
 * `false` once it has unmounted.
 *
 * Guard post-`await` `setState` calls with it in async action handlers: when the
 * awaited call resolves after the component has been navigated away from (or a
 * parent has swapped it out), the trailing `setState` / `finally { setState() }`
 * would otherwise run on a dead component. Prefer aborting the request with an
 * `AbortController` where that is natural; use this where it is not.
 */
export function useMountedRef() {
  const mounted = useRef(true);
  useEffect(() => {
    // Re-arm on mount so a StrictMode remount (or any future remount of the
    // same element) is handled correctly, not just the first mount.
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  return mounted;
}
