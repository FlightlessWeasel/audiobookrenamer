// Client-side grouping of an already-fetched book list, shared by the Books
// list and the Organize selection table so both offer the same "Group by"
// choices and identical bucketing rules.

import type { Book } from "../api/client";
import { statusLabel } from "./status";

export const GROUP_OPTIONS = [
  { value: "", label: "No grouping" },
  { value: "author", label: "Author" },
  { value: "series", label: "Series" },
  { value: "library", label: "Library" },
  { value: "state", label: "State" },
] as const;

export type GroupBy = (typeof GROUP_OPTIONS)[number]["value"];

export type BookGroup = { label: string; books: Book[] };

// Groups the already-fetched book list client-side. Returns null when no
// grouping is selected so the caller renders the flat list unchanged. Groups
// are sorted by label; within a group the server's ordering is preserved.
export function groupBooks(
  books: Book[],
  groupBy: GroupBy,
  libName: (id: string) => string,
): BookGroup[] | null {
  if (!groupBy) return null;

  const keyOf = (b: Book): { key: string; label: string } => {
    switch (groupBy) {
      case "author":
        return { key: b.author ?? "", label: b.author || "Unknown author" };
      case "series":
        return { key: b.series ?? "", label: b.series || "No series" };
      case "library":
        return { key: b.library_id, label: libName(b.library_id) };
      case "state":
        return { key: b.state, label: statusLabel(b.state) };
      default:
        return { key: "", label: "" };
    }
  };

  const groups = new Map<string, BookGroup>();
  for (const b of books) {
    const { key, label } = keyOf(b);
    let g = groups.get(key);
    if (!g) {
      g = { label, books: [] };
      groups.set(key, g);
    }
    g.books.push(b);
  }
  return [...groups.values()].sort((a, b) =>
    a.label.localeCompare(b.label, undefined, { sensitivity: "base" }),
  );
}

// The <select> option label for a grouping choice: "No grouping" as-is, the
// rest phrased as "Group by author".
export function groupOptionLabel(o: (typeof GROUP_OPTIONS)[number]): string {
  return o.value ? `Group by ${o.label.toLowerCase()}` : o.label;
}
