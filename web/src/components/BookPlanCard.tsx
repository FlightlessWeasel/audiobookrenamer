import { Link } from "react-router-dom";
import type { BookPlan, OrganizePlan } from "../api/client";

// One book's planned rename: the before/after for each file, or the reason it
// was skipped. Shared by the Organize page and the per-book Organize panel.
export function BookPlanCard({ plan }: { plan: BookPlan }) {
  return (
    <div
      className={`rounded-lg border p-3 text-sm ${
        plan.skip
          ? "border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-900/40"
          : "border-slate-200 dark:border-slate-800"
      }`}
    >
      <div className="flex items-center justify-between">
        <Link
          to={`/books/${plan.book_id}`}
          className="font-medium hover:underline"
          title="Open book details"
        >
          {plan.title}
        </Link>
        {plan.skip && <span className="text-xs text-amber-600">skipped — {plan.reason}</span>}
      </div>
      {!plan.skip && (
        <ul className="mt-2 space-y-1 font-mono text-xs">
          {plan.moves.map((m, i) => {
            const tf = plan.tag_files?.[i];
            return (
              <li key={i} className={m.no_op ? "text-slate-400" : ""}>
                <span className="text-slate-500">{m.from_rel}</span>
                <span className="mx-1">→</span>
                <span>{m.to_rel}</span>
                {m.no_op && <span className="ml-2 text-slate-400">(no change)</span>}
                {tf && !tf.writable && (
                  <span className="ml-2 text-slate-400" title={tf.reason}>
                    (tags not supported)
                  </span>
                )}
                {tf?.writable && tf.changed && (
                  <span className="ml-2 text-sky-600 dark:text-sky-400">(tags will update)</span>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

// Whether Apply has anything to do: any book that isn't skipped. A book whose
// files are already at their target path still has moves in the plan (all
// no_op) but is NOT skipped — it still needs Apply to run so its status can be
// finalized back to "organized". Gating on "some move is not a no-op" would
// leave a rematched, already-correctly-placed book stuck at "matched" with
// Apply permanently disabled for it.
export function planHasWork(plan: OrganizePlan): boolean {
  return plan.books.some((b) => !b.skip);
}

// Whether the plan actually moves a file, as opposed to only finalizing state.
export function planHasRealMoves(plan: OrganizePlan): boolean {
  return plan.books.some((b) => !b.skip && b.moves.some((m) => !m.no_op));
}

// Counts files across the plan whose tags Apply would rewrite. tag_files is
// only present at all when the library has write_tags on, so this is also
// how the page knows whether to show a tag-writing summary at all.
export function planTagWriteCount(plan: OrganizePlan): number {
  let n = 0;
  for (const b of plan.books) {
    if (b.skip || !b.tag_files) continue;
    for (const tf of b.tag_files) if (tf.writable && tf.changed) n++;
  }
  return n;
}
