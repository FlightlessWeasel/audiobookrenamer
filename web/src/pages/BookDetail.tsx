import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { errorMessage } from "../lib/errorMessage";
import {
  client,
  type Book,
  type Candidate,
  type ManualMatch,
  type OrganizePlan,
  type TagStatus,
} from "../api/client";
import { useAction } from "../lib/useAction";
import { useAsync } from "../lib/useAsync";
import { statusLabel } from "../lib/status";
import { tagMatchBadgeClass, tagMatchLabel, tagStatusDetail } from "../lib/tagStatus";
import { BookPlanCard, planHasRealMoves, planHasWork } from "../components/BookPlanCard";
import { waitForJob } from "../lib/waitForJob";

// parseYear returns a plausible 4-digit-ish year, or undefined for blank or
// non-numeric input, so a stray "abc" never gets sent as NaN/null.
function parseYear(raw: string): number | undefined {
  const s = raw.trim();
  if (!s) return undefined;
  const n = Number(s);
  return Number.isInteger(n) && n > 0 && n < 3000 ? n : undefined;
}

// candidateToManual is the single builder for the manual-match payload, used
// both when accepting a "manual" candidate and when accepting an off-provider
// result that isn't in the stored set.
function candidateToManual(c: Candidate): ManualMatch {
  return {
    title: c.title,
    subtitle: c.subtitle,
    author: c.authors?.[0],
    narrator: c.narrators?.join(", "),
    series: c.series,
    series_index: c.series_index,
    year: c.year,
    asin: c.asin,
    isbn: c.isbn,
    cover_url: c.cover_url,
  };
}

export function BookDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const book = useAsync((signal) => client.getBook(id, { signal }), [id]);
  const stored = useAsync(
    (signal) => client.listCandidates(id, { signal }),
    [id],
  );
  const settings = useAsync((signal) => client.getSettings({ signal }), []);

  // The manual-search provider picker is derived from the already-fetched
  // settings shape — no dedicated endpoint. Names match the Go provider IDs.
  const providerOptions = useMemo(() => {
    const s = settings.data;
    if (!s) return [] as { value: string; label: string }[];
    const out: { value: string; label: string }[] = [];
    if (s.audible.enabled) out.push({ value: "audible", label: "Audible" });
    if (s.google_books.enabled)
      out.push({ value: "googlebooks", label: "Google Books" });
    if (s.open_library.enabled)
      out.push({ value: "openlibrary", label: "Open Library" });
    return out;
  }, [settings.data]);

  const [searchResults, setSearchResults] = useState<Candidate[] | null>(null);
  // Per-candidate busy keys ("auto" for auto-match, "provider:id" per row) so
  // each Accept button spins independently.
  const { run, isBusy, error: err, mounted } = useAction();

  function reloadAll() {
    book.reload();
    stored.reload();
  }

  function auto() {
    run(async () => {
      const res = await client.autoMatch(id);
      if (!mounted.current) return;
      setSearchResults(null);
      if (res.candidates) stored.reload();
      book.reload();
    }, "auto");
  }

  function del() {
    const b = book.data;
    const name = b?.title || b?.source_file || b?.source_dir || "this book";
    if (
      !confirm(
        `Permanently delete ${name}?\n\nThe audio files are removed from disk and the record from the database. This cannot be undone.`,
      )
    ) {
      return;
    }
    run(async () => {
      await client.deleteBook(id);
      if (mounted.current) navigate("/books");
    }, "delete");
  }

  function accept(c: Candidate) {
    run(async () => {
      const inStore =
        c.provider !== "manual" &&
        stored.data?.some(
          (s) => s.provider === c.provider && s.provider_id === c.provider_id,
        );
      if (inStore) await client.acceptCandidate(id, c.provider, c.provider_id);
      else await client.acceptManual(id, candidateToManual(c));
      if (!mounted.current) return;
      reloadAll();
    }, `${c.provider}:${c.provider_id}`);
  }

  // The manual form submits a full replacement of the book's metadata, so it
  // hands over a complete ManualMatch and this just posts it.
  function acceptManualFields(m: ManualMatch) {
    run(async () => {
      await client.acceptManual(id, m);
      if (!mounted.current) return;
      reloadAll();
    }, "manual:manual");
  }

  // Only take over the whole page on the FIRST load. A reload() (after an
  // accept, a rematch, or an organize run) keeps showing the current book
  // until fresh data lands, so child panels — and any success message they
  // are holding — don't get torn down and remounted on every refresh.
  if (book.loading && !book.data) return <p className="text-sm text-slate-500">Loading…</p>;
  if (book.error && !book.data) return <p className="text-sm text-red-600">{book.error}</p>;
  if (!book.data) return null;
  const b = book.data;
  const candidates = searchResults ?? stored.data ?? [];

  return (
    <div className="space-y-6">
      <Link to="/books" className="text-sm text-slate-500 hover:underline">
        ← Books
      </Link>

      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">{b.title || "(untitled)"}</h1>
          <p className="text-sm text-slate-500">
            {b.source_file || b.source_dir}
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <button
            onClick={auto}
            disabled={isBusy("auto") || isBusy("delete")}
            className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
          >
            {isBusy("auto") ? "Matching…" : "Auto-match"}
          </button>
          <button
            onClick={del}
            disabled={isBusy("auto") || isBusy("delete")}
            className="rounded border border-red-300 px-3 py-1.5 text-sm font-medium text-red-700 disabled:opacity-50 dark:border-red-800 dark:text-red-400"
          >
            {isBusy("delete") ? "Deleting…" : "Delete"}
          </button>
        </div>
      </div>

      {err && (
        <p
          role="alert"
          className="rounded bg-red-100 px-3 py-2 text-sm text-red-800"
        >
          {err}
        </p>
      )}

      <dl className="grid max-w-xl grid-cols-[8rem_1fr] gap-x-4 gap-y-1 text-sm">
        <Row k="State" v={statusLabel(b.state)} />
        <Row k="Layout" v={b.layout} />
        <Row k="Author" v={b.author} />
        <Row k="Narrator" v={b.narrator} />
        <Row
          k="Series"
          v={b.series ? `${b.series} ${b.series_index ?? ""}`.trim() : ""}
        />
        <Row k="Year" v={b.year ? String(b.year) : ""} />
        <Row
          k="Match"
          v={
            b.matched_provider
              ? `${b.matched_provider} (${(b.match_score ?? 0).toFixed(2)})`
              : ""
          }
        />
      </dl>

      <TagStatusPanel key={`tags-${b.id}`} book={b} />

      <AuthorSortEditor
        key={b.id}
        current={b.author_sort ?? ""}
        derived={b.author_sort_source !== "manual"}
        fallback={b.author ?? ""}
        onSave={async (v) => {
          await client.updateBook(id, { author_sort: v });
          book.reload();
        }}
      />

      <OrganizePanel key={`organize-${b.id}`} book={b} onOrganized={reloadAll} />

      <ManualSearch
        book={b}
        providers={providerOptions}
        onResults={setSearchResults}
        onManual={acceptManualFields}
      />

      <section>
        <div className="mb-2 flex items-center gap-2">
          <h2 className="text-sm font-medium">
            {searchResults ? "Search results" : "Candidates"} (
            {candidates.length})
          </h2>
          {searchResults && (
            <button
              onClick={() => setSearchResults(null)}
              className="text-xs text-slate-500 hover:underline"
            >
              show stored
            </button>
          )}
        </div>
        <div className="space-y-2">
          {candidates.map((c) => (
            <CandidateRow
              key={`${c.provider}:${c.provider_id}`}
              c={c}
              busy={isBusy(`${c.provider}:${c.provider_id}`)}
              onAccept={() => accept(c)}
            />
          ))}
          {candidates.length === 0 && (
            <p className="text-sm text-slate-500">
              No candidates yet. Run auto-match or search above.
            </p>
          )}
        </div>
      </section>

      <div>
        <h2 className="mb-2 text-sm font-medium">
          Files ({b.files?.length ?? 0})
        </h2>
        <ul className="divide-y divide-slate-100 rounded border border-slate-200 text-xs dark:divide-slate-800 dark:border-slate-800">
          {b.files?.map((f) => (
            <li key={f.id} className="flex justify-between px-3 py-1.5">
              <span className="font-mono">{f.rel_path}</span>
              <span className="text-slate-400">
                {f.track ? `#${f.track} · ` : ""}
                {(f.size / 1_000_000).toFixed(1)} MB
              </span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

// OrganizePanel previews and applies the rename for this single book, so a
// book can be filed without going to the Organize tab and hunting for it. It
// drives the same /organize/preview + /organize/apply endpoints scoped to one
// book id.
function OrganizePanel({
  book,
  onOrganized,
}: {
  book: Book;
  onOrganized: () => void;
}) {
  const canOrganize = book.state === "matched" || book.state === "organized";
  const [plan, setPlan] = useState<OrganizePlan | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const { run, isBusy, error, mounted } = useAction();
  // Aborts an in-flight job poll when the panel unmounts (navigating away),
  // so waitForJob can't resolve onto a dead component.
  const poll = useRef<AbortController | null>(null);
  useEffect(() => () => poll.current?.abort(), []);

  // A rematch or sort-name edit changes the target path; drop a stale preview
  // so the user can't Apply a plan that no longer reflects the book. The
  // success message is left alone — the post-Apply reload lands here and must
  // not wipe the confirmation.
  useEffect(() => {
    setPlan(null);
  }, [book.updated_at]);

  function preview() {
    setMsg(null);
    run(async () => {
      const p = await client.organizePreview(book.library_id, [book.id]);
      if (mounted.current) setPlan(p);
    }, "preview");
  }

  function apply() {
    setMsg(null);
    run(async () => {
      const job = await client.organizeApply(book.library_id, [book.id]);
      // Organize runs as a background job: the book's state doesn't flip to
      // "organized" until the job finishes. Wait for it, then reload so this
      // page reflects the real outcome instead of the pre-run "matched".
      poll.current?.abort();
      const ctrl = new AbortController();
      poll.current = ctrl;
      let final;
      try {
        final = await waitForJob(job.id, { signal: ctrl.signal });
      } catch (e) {
        if (ctrl.signal.aborted) return; // panel unmounted mid-poll
        throw e;
      }
      if (!mounted.current) return;
      setPlan(null);
      onOrganized();
      if (final.status === "done") {
        setMsg("Renamed — this book is now organized.");
        return;
      }
      throw new Error(
        final.status === "canceled"
          ? "Organize job was canceled."
          : final.error || "Organize job failed.",
      );
    }, "apply");
  }

  const bp = plan?.books[0] ?? null;
  const hasWork = plan ? planHasWork(plan) : false;
  const hasRealMoves = plan ? planHasRealMoves(plan) : false;

  return (
    <section className="max-w-xl space-y-3 rounded-lg border border-slate-200 p-4 dark:border-slate-800">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium">Organize</h2>
        <button
          onClick={preview}
          disabled={!canOrganize || isBusy("preview") || isBusy("apply")}
          className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {isBusy("preview") ? "Building…" : plan ? "Rebuild preview" : "Preview rename"}
        </button>
        {plan && (
          <button
            onClick={apply}
            disabled={!hasWork || isBusy("apply")}
            className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-40 dark:border-slate-700"
          >
            {isBusy("apply") ? "Organizing…" : "Apply"}
          </button>
        )}
      </div>

      <p className="text-xs text-slate-500">
        Current state: <span className="font-medium">{statusLabel(book.state)}</span>
      </p>

      {!canOrganize && (
        <p className="text-sm text-slate-500">
          Match this book before organizing — only matched or organized books can be renamed.
        </p>
      )}
      {error && (
        <p role="alert" className="rounded bg-red-100 px-3 py-2 text-sm text-red-800">
          {error}
        </p>
      )}
      {msg && <p className="rounded bg-green-100 px-3 py-2 text-sm text-green-800">{msg}</p>}

      {plan && plan.conflicts && plan.conflicts.length > 0 && (
        <p className="rounded bg-amber-100 px-3 py-2 text-sm text-amber-800">
          Target path collides with another book; this book is skipped.
        </p>
      )}
      {plan && !hasWork && (
        <p className="text-sm text-slate-500">Nothing to do — this book was skipped.</p>
      )}
      {plan && hasWork && !hasRealMoves && (
        <p className="text-sm text-slate-500">
          Files are already in place — Apply just marks this book organized.
        </p>
      )}
      {bp && <BookPlanCard plan={bp} />}
    </section>
  );
}

// TagStatusPanel checks whether this book's file(s) currently carry embedded
// tags matching its accepted metadata. Fetched on demand — a button, not
// automatic on page load — because it reads the file(s) on disk, which for a
// large FLAC file in particular can take a moment.
function TagStatusPanel({ book }: { book: Book }) {
  const [status, setStatus] = useState<TagStatus | null>(null);
  const { run, busy, error, mounted } = useAction();

  function check() {
    run(async () => {
      const [result] = await client.tagStatus([book.id]);
      if (mounted.current) setStatus(result ?? null);
    });
  }

  return (
    <section className="max-w-xl space-y-3 rounded-lg border border-slate-200 p-4 dark:border-slate-800">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-sm font-medium">Embedded tags</h2>
        <button
          onClick={check}
          disabled={busy}
          className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
        >
          {busy ? "Checking…" : status ? "Recheck" : "Check tags"}
        </button>
        {status && (
          <span
            className={`rounded px-2 py-0.5 text-xs font-medium ${tagMatchBadgeClass(status.match)}`}
          >
            {tagMatchLabel(status.match)}
          </span>
        )}
      </div>
      {error && (
        <p role="alert" className="rounded bg-red-100 px-3 py-2 text-sm text-red-800">
          {error}
        </p>
      )}
      {status && (
        <>
          <p className="text-sm text-slate-500">{tagStatusDetail(status)}</p>
          {status.files && status.files.length > 0 && (
            <ul className="space-y-1 font-mono text-xs">
              {status.files.map((f) => (
                <li
                  key={f.file_rel}
                  className={f.writable && f.changed ? "text-amber-600 dark:text-amber-400" : "text-slate-500"}
                >
                  {f.file_rel}
                  {!f.writable && <span className="ml-2 text-slate-400">({f.reason})</span>}
                  {f.writable && f.changed && <span className="ml-2">— out of date</span>}
                  {f.writable && !f.changed && <span className="ml-2 text-slate-400">— up to date</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

function CandidateRow({
  c,
  busy,
  onAccept,
}: {
  c: Candidate;
  busy: boolean;
  onAccept: () => void;
}) {
  return (
    <div className="flex gap-3 rounded border border-slate-200 p-3 dark:border-slate-800">
      {c.cover_url ? (
        <img
          src={c.cover_url}
          alt=""
          className="h-20 w-14 shrink-0 rounded object-cover"
        />
      ) : (
        <div className="h-20 w-14 shrink-0 rounded bg-slate-100 dark:bg-slate-800" />
      )}
      <div className="min-w-0 flex-1 text-sm">
        <div className="font-medium">
          {c.title}
          {c.subtitle ? (
            <span className="text-slate-400"> — {c.subtitle}</span>
          ) : null}
        </div>
        <div className="text-slate-500">
          {(c.authors ?? []).join(", ")}
          {c.year ? ` · ${c.year}` : ""}
          {c.series ? ` · ${c.series} ${c.series_index ?? ""}` : ""}
        </div>
        <div className="mt-0.5 text-xs text-slate-400">
          {c.provider}
          {c.narrators?.length ? ` · narr. ${c.narrators.join(", ")}` : ""}
          {typeof c.score === "number" ? ` · score ${c.score.toFixed(2)}` : ""}
        </div>
      </div>
      <button
        onClick={onAccept}
        disabled={busy}
        className="shrink-0 self-center rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
      >
        {busy ? "…" : "Accept"}
      </button>
    </div>
  );
}

function ManualSearch({
  book,
  providers,
  onResults,
  onManual,
}: {
  book: Book;
  providers: { value: string; label: string }[];
  onResults: (c: Candidate[]) => void;
  onManual: (m: ManualMatch) => void;
}) {
  // Every field seeds from the book. Accepting replaces the book's metadata
  // wholesale, so a field left at "" because it was never populated would blank
  // what a provider had already supplied - which is how Audible matches lost
  // their series.
  const [title, setTitle] = useState(book.title ?? "");
  const [author, setAuthor] = useState(book.author ?? "");
  const [year, setYear] = useState(book.year ? String(book.year) : "");
  const [series, setSeries] = useState(book.series ?? "");
  const [seriesIndex, setSeriesIndex] = useState(book.series_index ?? "");
  const [provider, setProvider] = useState("");
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inflight = useRef<AbortController | null>(null);

  // Abort any outstanding search when this form unmounts (e.g. navigating to
  // another book) so a late response can't call setState on a dead component.
  useEffect(() => () => inflight.current?.abort(), []);

  async function run() {
    inflight.current?.abort();
    const ctrl = new AbortController();
    inflight.current = ctrl;
    setSearching(true);
    setError(null);
    try {
      const res = await client.searchMetadata(
        {
          title,
          author,
          year: parseYear(year),
          provider: provider || undefined,
        },
        { signal: ctrl.signal },
      );
      if (!ctrl.signal.aborted) onResults(res);
    } catch (e) {
      if (!ctrl.signal.aborted) setError(errorMessage(e));
    } finally {
      if (!ctrl.signal.aborted) setSearching(false);
    }
  }

  return (
    <section className="space-y-3 rounded-lg border border-slate-200 p-4 dark:border-slate-800">
      <h2 className="text-sm font-medium">Manual search / entry</h2>
      <div className="grid gap-2 sm:grid-cols-2">
        <Field label="Title" value={title} onChange={setTitle} />
        <Field label="Author" value={author} onChange={setAuthor} />
        <Field label="Year" value={year} onChange={setYear} />
        <Field label="Series" value={series} onChange={setSeries} />
        <Field label="Series #" value={seriesIndex} onChange={setSeriesIndex} />
        <label className="text-sm">
          <span className="mb-1 block text-slate-500">Provider</span>
          <select
            aria-label="Provider"
            value={provider}
            onChange={(e) => setProvider(e.target.value)}
            className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
          >
            <option value="">All enabled providers</option>
            {providers.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
      </div>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <button
          onClick={run}
          disabled={searching}
          className="rounded bg-slate-900 px-3 py-1.5 text-sm font-medium text-white disabled:opacity-50 dark:bg-slate-100 dark:text-slate-900"
        >
          {searching ? "Searching…" : "Search providers"}
        </button>
        <button
          onClick={() =>
            onManual({
              title,
              author,
              year: parseYear(year),
              series: series || undefined,
              series_index: seriesIndex || undefined,
              // Carried through, not edited here: the form does not show these,
              // and a replacement that omits them would silently clear them.
              subtitle: book.subtitle,
              narrator: book.narrator,
              asin: book.asin,
              isbn: book.isbn,
              cover_url: book.cover_url,
            })
          }
          className="rounded border border-slate-300 px-3 py-1.5 text-sm dark:border-slate-700"
        >
          Accept these fields
        </button>
      </div>
    </section>
  );
}

function Field({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="text-sm">
      <span className="mb-1 block text-slate-500">{label}</span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
      />
    </label>
  );
}

// AuthorSortEditor is the hand-editable author sort-name. Saving it marks the
// value as a manual override so a later metadata match won't recompute over it.
function AuthorSortEditor({
  current,
  derived,
  fallback,
  onSave,
}: {
  current: string;
  derived: boolean;
  fallback: string;
  onSave: (value: string) => Promise<void>;
}) {
  const [value, setValue] = useState(current);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      await onSave(value.trim());
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="max-w-xl space-y-2 rounded-lg border border-slate-200 p-4 dark:border-slate-800">
      <label className="block text-sm">
        <span className="mb-1 block text-slate-500">
          Author sort name{" "}
          <span className="text-xs text-slate-400">
            {derived ? "(derived — edit to override)" : "(manual override)"}
          </span>
        </span>
        <input
          aria-label="Author sort name"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={
            fallback
              ? `e.g. ${fallback.split(" ").slice(-1)[0]}, ${fallback}`
              : "Last, First"
          }
          className="w-full rounded border border-slate-300 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800"
        />
      </label>
      {error && (
        <p role="alert" className="text-sm text-red-600">
          {error}
        </p>
      )}
      <button
        onClick={save}
        disabled={saving || value.trim() === current.trim()}
        className="rounded border border-slate-300 px-3 py-1.5 text-sm disabled:opacity-50 dark:border-slate-700"
      >
        {saving ? "Saving…" : "Save sort name"}
      </button>
    </section>
  );
}

function Row({ k, v }: { k: string; v?: string }) {
  return (
    <>
      <dt className="text-slate-500">{k}</dt>
      <dd>{v || <span className="text-slate-400">—</span>}</dd>
    </>
  );
}
