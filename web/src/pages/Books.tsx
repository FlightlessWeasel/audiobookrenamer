import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { client, type AcceptOutcome, type Book } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import { useDebounced } from "../lib/useDebounced";
import { statusBadgeClass, statusLabel } from "../lib/status";

const STATES = ["unmatched", "needs_review", "matched", "organized", "error"] as const;

const GROUP_OPTIONS = [
  { value: "", label: "No grouping" },
  { value: "author", label: "Author" },
  { value: "series", label: "Series" },
  { value: "library", label: "Library" },
  { value: "state", label: "State" },
] as const;

type GroupBy = (typeof GROUP_OPTIONS)[number]["value"];

type BookGroup = { label: string; books: Book[] };

// Groups the already-fetched book list client-side. Returns null when no
// grouping is selected so the caller renders the flat list unchanged. Groups
// are sorted by label; within a group the server's ordering is preserved.
function groupBooks(
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

export function BooksPage() {
  const libs = useAsync((signal) => client.listLibraries({ signal }), []);
  const [libraryId, setLibraryId] = useState("");
  const [state, setState] = useState("");
  const [q, setQ] = useState("");
  const [groupBy, setGroupBy] = useState<GroupBy>("");
  // Debounce the search term so typing doesn't fire a listBooks request per
  // keystroke. useAsync still aborts the in-flight request when this changes.
  const debouncedQ = useDebounced(q, 300);

  const books = useAsync(
    (signal) => client.listBooks({ library_id: libraryId, state, q: debouncedQ }, { signal }),
    [libraryId, state, debouncedQ],
  );

  const libName = useMemo(() => {
    const m = new Map(libs.data?.map((l) => [l.id, l.name]));
    return (id: string) => m.get(id) ?? "—";
  }, [libs.data]);

  const counts = books.data?.counts ?? {};

  const groups = useMemo(
    () => groupBooks(books.data?.books ?? [], groupBy, libName),
    [books.data?.books, groupBy, libName],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-semibold">Books</h1>
        <button
          onClick={() => books.reload()}
          className="rounded border border-slate-300 px-2 py-1 text-xs dark:border-slate-700"
        >
          Refresh
        </button>
        <AcceptTopButton
          libraryId={libraryId}
          pending={(counts.unmatched ?? 0) + (counts.needs_review ?? 0)}
          onDone={() => books.reload()}
        />
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <select
          value={libraryId}
          onChange={(e) => setLibraryId(e.target.value)}
          aria-label="Filter by library"
          className="rounded border border-slate-300 px-2 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-800"
        >
          <option value="">All libraries</option>
          {libs.data?.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </select>

        <div className="flex gap-1">
          <FilterChip label="All" active={state === ""} onClick={() => setState("")} />
          {STATES.map((s) => (
            <FilterChip
              key={s}
              label={`${statusLabel(s)}${counts[s] ? ` ${counts[s]}` : ""}`}
              active={state === s}
              onClick={() => setState(s)}
            />
          ))}
        </div>

        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search title / author / path"
          aria-label="Search books"
          className="ml-auto w-64 rounded border border-slate-300 px-2 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-800"
        />

        <select
          value={groupBy}
          onChange={(e) => setGroupBy(e.target.value as GroupBy)}
          aria-label="Group books by"
          className="rounded border border-slate-300 px-2 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-800"
        >
          {GROUP_OPTIONS.map((o) => (
            <option key={o.value} value={o.value}>
              {o.value ? `Group by ${o.label.toLowerCase()}` : o.label}
            </option>
          ))}
        </select>
      </div>

      {books.loading && <p className="text-sm text-slate-500">Loading…</p>}
      {books.error && <p className="text-sm text-red-600">{books.error}</p>}

      <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
        <table className="w-full min-w-[48rem] text-sm">
          <thead className="bg-slate-100 text-left text-xs uppercase text-slate-500 dark:bg-slate-800">
            <tr>
              <th className="px-3 py-2">Title</th>
              <th className="px-3 py-2">Author</th>
              <th className="px-3 py-2">Library</th>
              <th className="px-3 py-2">Layout</th>
              <th className="px-3 py-2">Match</th>
              <th className="px-3 py-2">State</th>
            </tr>
          </thead>
          <tbody>
            {groups
              ? groups.map((g) => (
                  <Fragment key={g.label}>
                    <tr className="border-t border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/50">
                      <th
                        colSpan={6}
                        scope="colgroup"
                        className="px-3 py-1.5 text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                      >
                        {g.label}
                        <span className="ml-2 font-normal text-slate-400">{g.books.length}</span>
                      </th>
                    </tr>
                    {g.books.map((b) => (
                      <BookRow key={b.id} book={b} libName={libName} />
                    ))}
                  </Fragment>
                ))
              : books.data?.books.map((b: Book) => (
                  <BookRow key={b.id} book={b} libName={libName} />
                ))}
            {books.data?.books.length === 0 && (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-slate-500">
                  No books. Add a library and run a scan.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// Scores are 0..1 from the matcher; shown as whole percent so the column stays
// narrow, and coloured so a weak match is visible without reading the number.
function formatScore(score: number) {
  return `${Math.round(score * 100)}%`;
}

function scoreClass(score: number) {
  if (score >= 0.85) return "text-emerald-600 dark:text-emerald-400";
  if (score >= 0.6) return "text-amber-600 dark:text-amber-400";
  return "text-red-600 dark:text-red-400";
}

// AcceptTopButton bulk-accepts each unmatched / needs-review book's best stored
// candidate, for those scoring at or above the chosen bar. It works off
// candidates an earlier match already stored, so it never calls a provider and
// returns immediately rather than queuing a job.
function AcceptTopButton({
  libraryId,
  pending,
  onDone,
}: {
  libraryId: string;
  // Unmatched + needs-review books in scope. The button does nothing when
  // there are none, so it stays disabled until there is a backlog to clear.
  pending: number;
  onDone: () => void;
}) {
  const [minScore, setMinScore] = useState(0.7);
  const [result, setResult] = useState<AcceptOutcome | null>(null);
  const { run, busy, error, mounted } = useAction();
  const nothingToDo = pending === 0;

  function accept() {
    const scope = libraryId ? "this library" : "all libraries";
    if (
      !confirm(
        `Accept the best stored match for every unmatched or needs-review book in ${scope} scoring ${formatScore(minScore)} or better?`,
      )
    ) {
      return;
    }
    setResult(null);
    run(async () => {
      const out = await client.acceptTopCandidates(libraryId, minScore);
      if (mounted.current) setResult(out);
      onDone();
    });
  }

  return (
    <div className="flex items-center gap-2">
      <label className="flex items-center gap-1 text-xs text-slate-500">
        Min score
        <input
          type="number"
          min={0}
          max={1}
          step={0.05}
          value={minScore}
          onChange={(e) => setMinScore(Number(e.target.value))}
          aria-label="Minimum match score"
          className="w-16 rounded border border-slate-300 px-1.5 py-1 text-xs dark:border-slate-700 dark:bg-slate-800"
        />
      </label>
      <button
        onClick={accept}
        disabled={busy || nothingToDo}
        title={nothingToDo ? "No unmatched or needs-review books to accept" : undefined}
        className="rounded border border-slate-300 px-2 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
      >
        {busy ? "Accepting…" : "Auto-accept matches"}
      </button>
      {result && <span className={resultClass(result)}>{describeOutcome(result)}</span>}
      {error && <span className="text-xs text-red-600">{error}</span>}
    </div>
  );
}

// describeOutcome turns the accepted/no_candidates/below_score breakdown into
// a sentence that says WHY, not just how many. Zero accepted otherwise reads
// as the button having done nothing, when what actually happened is usually
// "none of these books have been searched yet".
function describeOutcome(out: AcceptOutcome): string {
  if (out.considered === 0) return "No unmatched or needs-review books to check.";
  if (out.accepted === out.considered) return `Accepted all ${out.accepted}.`;

  const reasons: string[] = [];
  if (out.no_candidates > 0) {
    reasons.push(`${out.no_candidates} not yet searched — run Match first`);
  }
  if (out.below_score > 0) {
    reasons.push(`${out.below_score} below the score bar`);
  }
  const suffix = reasons.length ? ` (${reasons.join("; ")})` : "";
  return `Accepted ${out.accepted} of ${out.considered}${suffix}`;
}

function resultClass(out: AcceptOutcome): string {
  // Flag the "looked like nothing happened" case distinctly from an ordinary
  // partial result: zero accepted with unsearched books is the state that was
  // reported as "not doing anything".
  if (out.accepted === 0 && out.no_candidates > 0) {
    return "text-xs text-amber-600 dark:text-amber-400";
  }
  return "text-xs text-slate-500";
}

function basename(b: Book) {
  const p = b.source_file || b.source_dir;
  return p.split(/[\\/]/).pop() ?? p;
}

function BookRow({ book: b, libName }: { book: Book; libName: (id: string) => string }) {
  return (
    <tr className="border-t border-slate-100 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50">
      <td className="px-3 py-2">
        <Link to={`/books/${b.id}`} className="font-medium hover:underline">
          {b.title || <span className="text-slate-400">{basename(b)}</span>}
        </Link>
        {b.series && (
          <span className="ml-2 text-xs text-slate-400">
            {b.series} {b.series_index}
          </span>
        )}
        <div className="truncate text-xs text-slate-400">{b.source_file || b.source_dir}</div>
      </td>
      <td className="px-3 py-2">{b.author || "—"}</td>
      <td className="px-3 py-2">{libName(b.library_id)}</td>
      <td className="px-3 py-2">{b.layout}</td>
      <td className="px-3 py-2 whitespace-nowrap">
        {b.matched_provider ? (
          <>
            <span className="text-slate-600 dark:text-slate-300">{b.matched_provider}</span>
            {b.match_score !== undefined && (
              <span className={`ml-2 text-xs ${scoreClass(b.match_score)}`}>
                {formatScore(b.match_score)}
              </span>
            )}
          </>
        ) : (
          <span className="text-slate-400">—</span>
        )}
      </td>
      <td className="px-3 py-2">
        <span className={`rounded px-2 py-0.5 text-xs font-medium ${statusBadgeClass(b.state)}`}>
          {statusLabel(b.state)}
        </span>
      </td>
    </tr>
  );
}

function FilterChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`rounded px-2 py-1.5 text-xs font-medium capitalize transition ${
        active
          ? "bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900"
          : "bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300"
      }`}
    >
      {label}
    </button>
  );
}
