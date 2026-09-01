// Single source of truth for turning a book `state` or a job `status` token into
// something renderable: a human-readable label and a Tailwind badge class.
// Previously every page re-implemented this — Books had `stateStyle`, Activity
// had `colors`, and several sites scattered ad-hoc `.replace("_", " ")` calls.

const BADGE_CLASSES: Record<string, string> = {
  // book states
  unmatched: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
  needs_review: "bg-amber-100 text-amber-800",
  matched: "bg-blue-100 text-blue-800",
  organized: "bg-green-100 text-green-800",
  error: "bg-red-100 text-red-800",
  // job statuses
  queued: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
  running: "bg-blue-100 text-blue-800",
  done: "bg-green-100 text-green-800",
  failed: "bg-red-100 text-red-800",
  canceled: "bg-amber-100 text-amber-800",
};

const FALLBACK_BADGE = "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200";

/**
 * Human-readable label for a status/state token: `"needs_review"` → `"needs
 * review"`. Blank/undefined becomes `"unknown"` so the UI never renders an empty
 * badge.
 */
export function statusLabel(status: string | null | undefined): string {
  if (!status) return "unknown";
  return status.replace(/_/g, " ");
}

/**
 * Tailwind classes for a status/state badge. Unknown tokens fall back to a
 * neutral style rather than producing an `undefined` class.
 */
export function statusBadgeClass(status: string | null | undefined): string {
  return (status != null && BADGE_CLASSES[status]) || FALLBACK_BADGE;
}
