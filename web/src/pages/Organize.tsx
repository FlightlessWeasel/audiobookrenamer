import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { client, type Book, type OrganizePlan } from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import {
  GROUP_OPTIONS,
  groupBooks,
  groupOptionLabel,
  type GroupBy,
} from "../lib/groupBooks";
import { useGroupBy } from "../lib/useGroupBy";
import { BookPlanCard, planHasRealMoves, planHasWork } from "../components/BookPlanCard";

export function OrganizePage() {
  const libs = useAsync((signal) => client.listLibraries({ signal }), []);
  const [libraryId, setLibraryId] = useState("");

  useEffect(() => {
    if (!libraryId && libs.data?.length) setLibraryId(libs.data[0].id);
  }, [libs.data, libraryId]);

  // The response is tagged with the library it was fetched for, so a response
  // that lands after the user has already switched libraries can be recognised
  // as stale and ignored.
  const matched = useAsync(
    (signal) =>
      libraryId
        ? client
            .listBooks({ library_id: libraryId, state: "matched" }, { signal })
            .then((r) => ({ forLib: libraryId, books: r.books }))
        : Promise.resolve(null),
    [libraryId],
  );

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [groupBy, setGroupBy] = useGroupBy();
  const [plan, setPlan] = useState<OrganizePlan | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  // "preview" and "apply" are independent busy flags; the buttons already
  // block one while the other runs.
  const { run, busy: acting, isBusy, error: err, clearError, mounted } = useAction();

  // Tracks which library the current selection belongs to, so a plain reload
  // (e.g. after Apply) doesn't blow away the user's picks.
  const selectionLib = useRef<string>("");

  // Switching library invalidates any selection/plan built against the old one
  // immediately, before the new book list arrives.
  useEffect(() => {
    setSelected(new Set());
    setPlan(null);
    setMsg(null);
    clearError();
  }, [libraryId, clearError]);

  // Fresh data means: not loading, and the response is for the library that is
  // currently selected (not a stale response from a previous selection).
  const fresh = !matched.loading && matched.data?.forLib === libraryId ? matched.data : null;
  const loadingBooks = !fresh && !!libraryId;
  const books = fresh?.books ?? [];

  useEffect(() => {
    if (!fresh) return; // wait for the real, current list before touching selection
    const ids = fresh.books.map((b) => b.id);
    setSelected((prev) => {
      if (selectionLib.current !== libraryId) {
        selectionLib.current = libraryId;
        return new Set(ids); // first load for this library → select all
      }
      // Same library reloaded → keep the user's selection, minus any books
      // that no longer exist.
      const next = new Set<string>();
      for (const id of ids) if (prev.has(id)) next.add(id);
      return next;
    });
    setPlan(null);
  }, [fresh, libraryId]); // eslint-disable-line react-hooks/exhaustive-deps

  const bookIds = useMemo(() => [...selected], [selected]);

  const libName = useMemo(() => {
    const m = new Map(libs.data?.map((l) => [l.id, l.name]));
    return (id: string) => m.get(id) ?? "—";
  }, [libs.data]);

  const groups = useMemo(
    () => groupBooks(books, groupBy, libName),
    [books, groupBy, libName],
  );

  function preview() {
    if (loadingBooks) return;
    setMsg(null);
    run(async () => {
      const p = await client.organizePreview(libraryId, bookIds);
      if (mounted.current) setPlan(p);
    }, "preview");
  }

  function apply() {
    if (loadingBooks) return;
    run(async () => {
      await client.organizeApply(libraryId, bookIds);
      if (!mounted.current) return;
      setMsg("Organize job queued — watch the Activity tab.");
      setPlan(null);
      matched.reload();
    }, "apply");
  }

  function toggle(id: string) {
    setSelected((s) => {
      const n = new Set(s);
      n.has(id) ? n.delete(id) : n.add(id);
      return n;
    });
  }

  const hasWork = plan ? planHasWork(plan) : false;
  const hasRealMoves = plan ? planHasRealMoves(plan) : false;

  return (
    <div className="space-y-5">
      <h1 className="text-xl font-semibold">Organize</h1>

      <div className="flex flex-wrap items-center gap-2">
        <select
          value={libraryId}
          onChange={(e) => setLibraryId(e.target.value)}
          aria-label="Organize library"
          className="rounded border border-slate-300 px-2 py-1.5 text-sm dark:border-slate-700 dark:bg-slate-800"
        >
          {libs.data?.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </select>
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
        <span className="text-sm text-slate-500">
          {loadingBooks ? "loading books…" : `${selected.size} of ${books.length} matched books`}
        </span>
        <button
          onClick={preview}
          disabled={!libraryId || selected.size === 0 || acting || loadingBooks}
          className="ml-auto rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {isBusy("preview") ? "Building…" : "Preview"}
        </button>
        <button
          onClick={apply}
          disabled={!plan || !hasWork || acting || loadingBooks}
          className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-40 dark:border-slate-700"
        >
          {isBusy("apply") ? "Queuing…" : "Apply"}
        </button>
      </div>

      {msg && <p className="rounded bg-green-100 px-3 py-2 text-sm text-green-800">{msg}</p>}
      {err && <p className="rounded bg-red-100 px-3 py-2 text-sm text-red-800">{err}</p>}

      {!plan && (
        <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
          <table className="w-full text-sm">
            <tbody>
              {groups
                ? groups.map((g) => (
                    <Fragment key={g.label}>
                      <tr className="border-t border-slate-200 bg-slate-50 first:border-t-0 dark:border-slate-700 dark:bg-slate-800/50">
                        <th
                          colSpan={2}
                          scope="colgroup"
                          className="px-3 py-1.5 text-left text-xs font-semibold uppercase tracking-wide text-slate-500"
                        >
                          {g.label}
                          <span className="ml-2 font-normal text-slate-400">{g.books.length}</span>
                        </th>
                      </tr>
                      {g.books.map((b) => (
                        <SelectRow
                          key={b.id}
                          book={b}
                          checked={selected.has(b.id)}
                          onToggle={() => toggle(b.id)}
                        />
                      ))}
                    </Fragment>
                  ))
                : books.map((b) => (
                    <SelectRow
                      key={b.id}
                      book={b}
                      checked={selected.has(b.id)}
                      onToggle={() => toggle(b.id)}
                    />
                  ))}
              {books.length === 0 && (
                <tr>
                  <td className="px-3 py-6 text-center text-slate-500">
                    No matched books in this library. Match some first.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {plan && (
        <div className="space-y-3">
          <button onClick={() => setPlan(null)} className="text-sm text-slate-500 hover:underline">
            ← back to selection
          </button>
          {plan.conflicts && plan.conflicts.length > 0 && (
            <p className="rounded bg-amber-100 px-3 py-2 text-sm text-amber-800">
              {plan.conflicts.length} path collision(s); affected books are skipped.
            </p>
          )}
          {!hasWork && (
            <p className="text-sm text-slate-500">Nothing to do — every selected book was skipped.</p>
          )}
          {hasWork && !hasRealMoves && (
            <p className="text-sm text-slate-500">
              No files need to move — Apply will just update these books' status to organized.
            </p>
          )}
          {plan.books.map((bp) => (
            <BookPlanCard key={bp.book_id} plan={bp} />
          ))}
        </div>
      )}
    </div>
  );
}

function SelectRow({
  book: b,
  checked,
  onToggle,
}: {
  book: Book;
  checked: boolean;
  onToggle: () => void;
}) {
  return (
    <tr className="border-t border-slate-100 first:border-t-0 dark:border-slate-800">
      <td className="w-10 px-3 py-2">
        <input
          type="checkbox"
          aria-label={`Select ${b.title || b.id}`}
          checked={checked}
          onChange={onToggle}
        />
      </td>
      <td className="px-3 py-2">
        <div className="font-medium">{b.title}</div>
        <div className="text-xs text-slate-400">
          {b.author}
          {b.series ? ` · ${b.series} ${b.series_index ?? ""}` : ""} · {b.layout}
        </div>
      </td>
    </tr>
  );
}
