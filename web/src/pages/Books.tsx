import { Fragment, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { client, type AcceptOutcome, type Book, type TagStatus } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import { useDebounced } from "../lib/useDebounced";
import { canOrganizeBook, statusBadgeClass, statusLabel } from "../lib/status";
import { tagMatchBadgeClass, tagMatchLabel, tagStatusDetail } from "../lib/tagStatus";
import {
  GROUP_OPTIONS,
  groupBooks,
  groupOptionLabel,
  type GroupBy,
} from "../lib/groupBooks";
import { useGroupBy } from "../lib/useGroupBy";
import { formatScore, scoreClass } from "../lib/matchScore";
import { TriStateCheckbox } from "../components/TriStateCheckbox";
import { waitForJob } from "../lib/waitForJob";

const STATES = ["unmatched", "needs_review", "matched", "organized", "error"] as const;

// Checking tags means reading every listed file on disk, so it is an explicit
// action rather than something that fires on every list load. One request can
// only carry this many ids (see maxTagStatusIDs in internal/api/books_tags.go),
// so a longer list is walked in chunks of this size rather than in one call.
const MAX_TAG_CHECK = 200;

export function BooksPage() {
  const libs = useAsync((signal) => client.listLibraries({ signal }), []);
  const [libraryId, setLibraryId] = useState("");
  const [state, setState] = useState("");
  const [q, setQ] = useState("");
  const [groupBy, setGroupBy] = useGroupBy();
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
  const libRoot = useMemo(() => {
    const m = new Map(libs.data?.map((l) => [l.id, l.root_path]));
    return (id: string) => m.get(id) ?? "";
  }, [libs.data]);

  const counts = books.data?.counts ?? {};
  const allBooks = useMemo(() => books.data?.books ?? [], [books.data?.books]);

  const groups = useMemo(
    () => groupBooks(allBooks, groupBy, libName),
    [allBooks, groupBy, libName],
  );

  // Multi-select for bulk delete. The set is pruned to the currently-listed
  // books whenever the list changes (a filter, a search, a reload), so a stale
  // id can never be sent to the delete endpoint.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  useEffect(() => {
    setSelected((prev) => {
      if (prev.size === 0) return prev;
      const live = new Set(allBooks.map((b) => b.id));
      const next = new Set<string>();
      for (const id of prev) if (live.has(id)) next.add(id);
      return next.size === prev.size ? prev : next;
    });
  }, [allBooks]);

  const { run: runDelete, busy: deleting, error: deleteErr, mounted } = useAction();
  const [deleteMsg, setDeleteMsg] = useState<string | null>(null);

  // Tag status is checked on demand, not fetched with the list: it reads every
  // listed file on disk, which the list itself never does. Keyed by book id so
  // a reload or a filter change doesn't have to throw known results away.
  const [tagStatuses, setTagStatuses] = useState<Map<string, TagStatus>>(new Map());
  const { run: runTagCheck, busy: checkingTags, error: tagCheckErr } = useAction();

  // Shared by checkTags and retagSelected: a request can only carry
  // MAX_TAG_CHECK ids, so this walks the full list in chunks rather than
  // refusing to run at all above that count.
  async function refreshTagStatuses(ids: string[]) {
    for (let i = 0; i < ids.length; i += MAX_TAG_CHECK) {
      const results = await client.tagStatus(ids.slice(i, i + MAX_TAG_CHECK));
      if (!mounted.current) return;
      setTagStatuses((prev) => {
        const next = new Map(prev);
        for (const r of results) next.set(r.id, r);
        return next;
      });
    }
  }

  function checkTags() {
    const ids = allBooks.map((b) => b.id);
    if (ids.length === 0) return;
    runTagCheck(() => refreshTagStatuses(ids));
  }

  // Runs organize for every selected, retaggable book, one job per library
  // (organize/apply is scoped to a single library): rewrites tags where the
  // library has write_tags on and they differ, exactly what "Retag now" does
  // for one book on the detail page.
  const { run: runRetag, busy: retagging, error: retagErr } = useAction();
  const [retagMsg, setRetagMsg] = useState<string | null>(null);

  function retagSelected() {
    const byID = new Map(allBooks.map((b) => [b.id, b]));
    const ids = [...selected];
    const eligible = ids.filter((id) => canOrganizeBook(byID.get(id)?.state));
    const skipped = ids.length - eligible.length;
    if (eligible.length === 0) {
      setRetagMsg("None of the selected books can be retagged (only matched or organized books can be).");
      return;
    }
    if (
      !confirm(
        `Retag ${eligible.length} book${eligible.length === 1 ? "" : "s"}` +
          (skipped ? ` (${skipped} selected book${skipped === 1 ? "" : "s"} skipped — not matched)` : "") +
          `?\n\nThis rewrites embedded tags for any of them whose library has tag writing on and whose tags differ.`,
      )
    ) {
      return;
    }
    setRetagMsg(null);
    runRetag(async () => {
      const byLibrary = new Map<string, string[]>();
      for (const id of eligible) {
        const libraryId = byID.get(id)!.library_id;
        const group = byLibrary.get(libraryId);
        if (group) group.push(id);
        else byLibrary.set(libraryId, [id]);
      }
      const groups = [...byLibrary.entries()];
      const jobs = await Promise.all(
        groups.map(([libraryId, bookIds]) => client.organizeApply(libraryId, bookIds)),
      );
      const finals = await Promise.all(jobs.map((j) => waitForJob(j.id)));
      if (!mounted.current) return;

      books.reload();
      await refreshTagStatuses(eligible);
      if (!mounted.current) return;
      setSelected(new Set());

      // Each library's job is all-or-nothing, so a failed job's whole group of
      // book ids failed with it — tallied in books, not libraries, to match
      // every other count on this page (Deleted N, checked N ids, ...).
      const failedGroups = groups.filter((_, i) => finals[i].status !== "done");
      const failedBooks = failedGroups.reduce((n, [, bookIds]) => n + bookIds.length, 0);
      const skippedNote = skipped ? ` (${skipped} skipped — not matched)` : "";
      if (failedGroups.length === 0) {
        setRetagMsg(`Retagged ${eligible.length} book${eligible.length === 1 ? "" : "s"}${skippedNote}.`);
        return;
      }
      const firstFailed = finals[groups.findIndex(([libraryId]) => libraryId === failedGroups[0][0])];
      throw new Error(
        `Retagged ${eligible.length - failedBooks} of ${eligible.length} book${eligible.length === 1 ? "" : "s"}` +
          `${skippedNote}; ${failedBooks} failed (${firstFailed.error || firstFailed.status}).`,
      );
    });
  }

  function toggle(id: string) {
    setSelected((s) => {
      const n = new Set(s);
      n.has(id) ? n.delete(id) : n.add(id);
      return n;
    });
  }
  function setMany(ids: string[], on: boolean) {
    setSelected((s) => {
      const n = new Set(s);
      for (const id of ids) on ? n.add(id) : n.delete(id);
      return n;
    });
  }

  const allIds = allBooks.map((b) => b.id);
  const allSelected = allIds.length > 0 && allIds.every((id) => selected.has(id));
  const someSelected = allIds.some((id) => selected.has(id));

  function deleteSelected() {
    const ids = [...selected];
    if (ids.length === 0) return;
    if (
      !confirm(
        `Permanently delete ${ids.length} book${ids.length === 1 ? "" : "s"}?\n\n` +
          `The audio files are removed from disk and the records from the database. This cannot be undone.`,
      )
    ) {
      return;
    }
    setDeleteMsg(null);
    runDelete(async () => {
      const res = await client.deleteBooks(ids);
      if (!mounted.current) return;
      setSelected(new Set());
      books.reload();
      const failed = res.failed?.length ?? 0;
      setDeleteMsg(
        failed === 0
          ? `Deleted ${res.deleted} book${res.deleted === 1 ? "" : "s"}.`
          : `Deleted ${res.deleted}; ${failed} could not be removed (${res.failed?.[0]?.error ?? "unknown error"}).`,
      );
    });
  }

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
        <button
          onClick={checkTags}
          disabled={checkingTags || allBooks.length === 0}
          title={
            allBooks.length > MAX_TAG_CHECK
              ? `Reads each listed file's embedded tags in batches of ${MAX_TAG_CHECK}`
              : "Reads each listed file's embedded tags and compares them to this book's accepted metadata"
          }
          className="rounded border border-slate-300 px-2 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
        >
          {checkingTags ? "Checking tags…" : "Check tags"}
        </button>
        {tagCheckErr && <span className="text-xs text-red-600">{tagCheckErr}</span>}
      </div>

      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-200 px-3 py-2 dark:border-slate-800">
        <label className="flex items-center gap-2 text-sm text-slate-500">
          <TriStateCheckbox
            checked={allSelected}
            indeterminate={someSelected && !allSelected}
            disabled={allBooks.length === 0}
            onChange={() => setMany(allIds, !allSelected)}
            label="Select all listed books"
          />
          <span>{selected.size} selected</span>
        </label>
        <button
          onClick={retagSelected}
          disabled={selected.size === 0 || retagging}
          title="Rewrites embedded tags for the selected books wherever their library has tag writing on and the tags differ"
          className="rounded border border-slate-300 px-3 py-1.5 text-sm font-medium disabled:opacity-40 dark:border-slate-700"
        >
          {retagging ? "Retagging…" : "Retag selected"}
        </button>
        {retagMsg && <span className="text-xs text-slate-500">{retagMsg}</span>}
        {retagErr && <span className="text-xs text-red-600">{retagErr}</span>}
        <button
          onClick={deleteSelected}
          disabled={selected.size === 0 || deleting}
          className="rounded border border-red-300 px-3 py-1.5 text-sm font-medium text-red-700 disabled:opacity-40 dark:border-red-800 dark:text-red-400"
        >
          {deleting ? "Deleting…" : "Delete selected"}
        </button>
        {deleteMsg && <span className="text-xs text-slate-500">{deleteMsg}</span>}
        {deleteErr && <span className="text-xs text-red-600">{deleteErr}</span>}
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
              {groupOptionLabel(o)}
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
              <th className="w-10 px-3 py-2"></th>
              <th className="px-3 py-2">Title</th>
              <th className="px-3 py-2">Author</th>
              <th className="px-3 py-2">Library</th>
              <th className="px-3 py-2">Layout</th>
              <th className="px-3 py-2">Match</th>
              <th className="px-3 py-2">State</th>
              <th className="px-3 py-2">Tags</th>
            </tr>
          </thead>
          <tbody>
            {groups
              ? groups.map((g) => {
                  const gIds = g.books.map((b) => b.id);
                  const gAll = gIds.every((id) => selected.has(id));
                  const gSome = gIds.some((id) => selected.has(id));
                  return (
                    <Fragment key={g.label}>
                      <tr className="border-t border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/50">
                        <th
                          colSpan={8}
                          scope="colgroup"
                          className="px-3 py-1.5 text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                        >
                          <label className="flex items-center gap-2 normal-case">
                            <TriStateCheckbox
                              checked={gAll}
                              indeterminate={gSome && !gAll}
                              onChange={() => setMany(gIds, !gAll)}
                              label={`Select all in ${g.label}`}
                            />
                            <span>{g.label}</span>
                            <span className="font-normal text-slate-400">{g.books.length}</span>
                          </label>
                        </th>
                      </tr>
                      {g.books.map((b) => (
                        <BookRow
                          key={b.id}
                          book={b}
                          libName={libName}
                          libRoot={libRoot}
                          checked={selected.has(b.id)}
                          onToggle={() => toggle(b.id)}
                          tagStatus={tagStatuses.get(b.id)}
                        />
                      ))}
                    </Fragment>
                  );
                })
              : allBooks.map((b: Book) => (
                  <BookRow
                    key={b.id}
                    book={b}
                    libName={libName}
                    libRoot={libRoot}
                    checked={selected.has(b.id)}
                    onToggle={() => toggle(b.id)}
                    tagStatus={tagStatuses.get(b.id)}
                  />
                ))}
            {allBooks.length === 0 && (
              <tr>
                <td colSpan={8} className="px-3 py-6 text-center text-slate-500">
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

// The book's source path as stored is absolute on the server's filesystem,
// which is mostly noise in a list of many books; strip the library's own
// root off it so what's left is just where the book lives inside that
// library. Falls back to the full path if it doesn't start with root (e.g.
// root not loaded yet), rather than guessing at a wrong prefix.
function libRelativePath(path: string, root: string): string {
  const norm = (p: string) => p.replace(/\\/g, "/").replace(/\/+$/, "");
  const p = norm(path);
  const r = norm(root);
  if (!r) return p;
  if (p === r) return ".";
  return p.startsWith(r + "/") ? p.slice(r.length + 1) : p;
}

function BookRow({
  book: b,
  libName,
  libRoot,
  checked,
  onToggle,
  tagStatus,
}: {
  book: Book;
  libName: (id: string) => string;
  libRoot: (id: string) => string;
  checked: boolean;
  onToggle: () => void;
  tagStatus?: TagStatus;
}) {
  return (
    <tr className="border-t border-slate-100 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800/50">
      <td className="w-10 px-3 py-2">
        <input
          type="checkbox"
          aria-label={`Select ${b.title || b.id}`}
          checked={checked}
          onChange={onToggle}
        />
      </td>
      <td className="px-3 py-2">
        <Link to={`/books/${b.id}`} className="font-medium hover:underline">
          {b.title || <span className="text-slate-400">{basename(b)}</span>}
        </Link>
        {(b.subtitle || b.series) && (
          <div className="truncate text-xs text-slate-400">
            {[b.subtitle, b.series && `${b.series}${b.series_index ? ` ${b.series_index}` : ""}`]
              .filter(Boolean)
              .join(" · ")}
          </div>
        )}
        <div className="truncate text-xs text-slate-400">
          {libRelativePath(b.source_file || b.source_dir, libRoot(b.library_id))}
        </div>
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
      <td className="px-3 py-2">
        {tagStatus ? (
          <span
            className={`rounded px-2 py-0.5 text-xs font-medium ${tagMatchBadgeClass(tagStatus.match)}`}
            title={tagStatusDetail(tagStatus)}
          >
            {tagMatchLabel(tagStatus.match)}
          </span>
        ) : (
          <span className="text-xs text-slate-400">—</span>
        )}
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
