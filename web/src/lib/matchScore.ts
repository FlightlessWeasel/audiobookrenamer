// Match-score formatting shared by the Books list and the Organize selection
// table so a score renders identically in both places.
//
// Scores are 0..1 from the matcher; shown as whole percent so the column stays
// narrow, and coloured so a weak match is visible without reading the number.

export function formatScore(score: number) {
  return `${Math.round(score * 100)}%`;
}

export function scoreClass(score: number) {
  if (score >= 0.85) return "text-emerald-600 dark:text-emerald-400";
  if (score >= 0.6) return "text-amber-600 dark:text-amber-400";
  return "text-red-600 dark:text-red-400";
}
