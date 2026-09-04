// Single source of truth for rendering a TagStatus.match value, shared by the
// Books list and the book detail page so the two surfaces never drift.

import type { TagStatus } from "../api/client";

const LABELS: Record<TagStatus["match"], string> = {
  match: "tags match",
  mismatch: "tags differ",
  unsupported: "format unsupported",
  unmatched: "not matched yet",
};

const BADGE_CLASSES: Record<TagStatus["match"], string> = {
  match: "bg-green-100 text-green-800",
  mismatch: "bg-amber-100 text-amber-800",
  unsupported: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
  unmatched: "bg-slate-200 text-slate-700 dark:bg-slate-700 dark:text-slate-200",
};

export function tagMatchLabel(match: TagStatus["match"]): string {
  return LABELS[match];
}

export function tagMatchBadgeClass(match: TagStatus["match"]): string {
  return BADGE_CLASSES[match];
}

// One line explaining a status, for a tooltip or a detail panel: names which
// file(s) are affected when that's informative, and whether write_tags is
// even on (a "mismatch" while it's off just means turning it on would change
// something, not that anything is broken).
export function tagStatusDetail(status: TagStatus): string {
  if (status.error) return status.error;
  if (status.match === "unmatched") return "This book has no accepted metadata yet.";
  if (status.match === "unsupported") {
    return "None of this book's files have a supported tag-writing format.";
  }
  const changed = (status.files ?? []).filter((f) => f.writable && f.changed).map((f) => f.file_rel);
  if (status.match === "mismatch") {
    const suffix = status.enabled ? "" : " (write_tags is off for this library, so nothing will change on its own)";
    return changed.length
      ? `Out of date: ${changed.join(", ")}${suffix}`
      : `Tags are out of date${suffix}`;
  }
  return "Every writable file's tags match this book's accepted metadata.";
}
