/**
 * The message to show a user for a value thrown from an async action.
 *
 * `catch` binds `unknown`, so every call site has to narrow before it can read
 * `.message`. Doing that inline in each one is how the four copies of this
 * ternary drifted apart; keep the narrowing here so "how an error is worded"
 * stays one decision.
 */
export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
