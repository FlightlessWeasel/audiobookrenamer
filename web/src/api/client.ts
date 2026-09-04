// Thin typed wrapper over the JSON API. All calls go through api() so auth and
// error handling stay in one place.

import { errorMessage } from "../lib/errorMessage";

export interface Library {
  id: string;
  name: string;
  root_path: string;
  structure_mode: "author_first" | "series_first";
  // Which form of the author's name names the author folder: "sort" =>
  // "Campbell, Jack", "name" => "Jack Campbell".
  author_folder_mode: "sort" | "name";
  file_template: string;
  multi_file_template: string;
  enabled: boolean;
  // Rewrite embedded audio-file tags during organize; embed_cover additionally
  // embeds cover art and only applies while write_tags is on. Both default off.
  write_tags: boolean;
  embed_cover: boolean;
  created_at: string;
  updated_at: string;
}

export interface BookFile {
  id: string;
  rel_path: string;
  size: number;
  mod_time: number;
  ext: string;
  track?: number;
}

export interface Book {
  id: string;
  library_id: string;
  source_dir: string;
  source_file?: string;
  layout: "single" | "multi";
  state: "unmatched" | "needs_review" | "matched" | "organized" | "error";
  message?: string;
  match_score?: number;
  matched_provider?: string;
  title?: string;
  subtitle?: string;
  author?: string;
  author_sort?: string;
  author_sort_source?: "derived" | "manual";
  narrator?: string;
  series?: string;
  series_index?: string;
  year?: number;
  asin?: string;
  isbn?: string;
  cover_url?: string;
  files?: BookFile[];
  updated_at: string;
}

// What a bulk accept-top-candidates run did. considered === accepted +
// no_candidates + below_score: every book in scope lands in exactly one
// bucket, which is what lets the UI explain a 0-accepted run instead of it
// looking like the button did nothing.
export interface AcceptOutcome {
  considered: number;
  accepted: number;
  no_candidates: number;
  below_score: number;
}

export interface BooksResponse {
  books: Book[];
  counts: Record<string, number>;
}

// Outcome of a bulk book delete. `deleted` is how many were removed from disk
// and the database; `failed` lists the rest with the reason (e.g. a path that
// resolved outside the library root), so a partial run can be explained.
export interface DeleteBooksResult {
  deleted: number;
  failed?: { id: string; title?: string; error: string }[];
}

export interface Candidate {
  provider: string;
  provider_id: string;
  title: string;
  subtitle?: string;
  authors?: string[];
  narrators?: string[];
  series?: string;
  series_index?: string;
  year?: number;
  asin?: string;
  isbn?: string;
  cover_url?: string;
  score?: number;
}

export interface ManualMatch {
  title?: string;
  subtitle?: string;
  author?: string;
  narrator?: string;
  series?: string;
  series_index?: string;
  year?: number;
  asin?: string;
  isbn?: string;
  cover_url?: string;
}

export interface MatchResponse {
  book: Book;
  candidates?: Candidate[];
}

export interface FileMove {
  from_rel: string;
  to_rel: string;
  no_op: boolean;
}

// TagFilePlan is the tag-rewrite outcome planned for one file, at the same
// index as the matching entry in BookPlan.moves. Present only when the
// library has write_tags on.
export interface TagFilePlan {
  file_rel: string;
  writable: boolean;
  changed: boolean;
  reason?: string;
}

export interface BookPlan {
  book_id: string;
  title: string;
  moves: FileMove[];
  skip: boolean;
  reason?: string;
  tag_files?: TagFilePlan[];
}

export interface OrganizePlan {
  library_id: string;
  root_path: string;
  books: BookPlan[];
  conflicts?: string[];
}

export interface Job {
  id: string;
  type: "scan" | "match" | "organize" | "undo";
  status: "queued" | "running" | "done" | "failed" | "canceled";
  library_id?: string;
  total: number;
  done: number;
  message?: string;
  error?: string;
  created_at: string;
  finished_at?: string;
}

export interface Settings {
  auto_match_threshold: number;
  audible: { enabled: boolean; region: string };
  google_books: { enabled: boolean; api_key_set: boolean };
  open_library: { enabled: boolean };
  // api_key is returned only by the PATCH that generated or rotated it — the
  // server has no endpoint that reads it back, so this response is the
  // operator's one chance to copy it. A GET never carries it.
  auth: {
    enabled: boolean;
    username?: string;
    api_key_set: boolean;
    api_key?: string;
  };
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

function notifyIfUnauthorized(status: number, path: string) {
  if (status === 401 && !path.startsWith("/auth/")) {
    window.dispatchEvent(new CustomEvent("abr:unauthorized"));
  }
}

/**
 * A runtime validator for a response body. Return the value (optionally
 * narrowed/normalised) when it is the expected shape, or throw to reject it.
 * Only the security- or safety-critical endpoints pass one today; the rest
 * still trust the server's contract.
 */
type Validator<T> = (raw: unknown) => T;

async function api<T>(
  path: string,
  init?: RequestInit,
  validate?: Validator<T>,
): Promise<T> {
  const res = await fetch(`/api${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(init?.headers ?? {}),
    },
  });
  if (res.status === 204) return undefined as T;

  const text = await res.text();
  let body: unknown;
  if (text) {
    try {
      body = JSON.parse(text);
    } catch {
      // A proxy/gateway error page (HTML, plain text) reaches here. Surface it
      // as an ApiError with the HTTP status rather than letting a raw
      // SyntaxError escape.
      notifyIfUnauthorized(res.status, path);
      throw new ApiError(
        res.ok ? 502 : res.status,
        res.ok
          ? "malformed response from server"
          : `${res.status} ${res.statusText}`.trim(),
      );
    }
  }

  if (!res.ok) {
    notifyIfUnauthorized(res.status, path);
    const msg =
      body && typeof body === "object" && "error" in body
        ? String((body as { error: unknown }).error)
        : res.statusText || `request failed (${res.status})`;
    throw new ApiError(res.status, msg);
  }
  if (validate) {
    try {
      return validate(body);
    } catch (e) {
      // The validators name the field that failed ("books[3].id: expected
      // string"). Dropping that leaves a bare 502 that nobody can act on.
      throw new ApiError(
        502,
        `unexpected response shape from server: ${errorMessage(e)}`,
      );
    }
  }
  return body as T;
}

// ---------------------------------------------------------------------------
// Runtime response validation
//
// Hand-written, dependency-free structural checks for the response shapes the
// UI actually depends on. They THROW on structurally broken data (missing ids,
// wrong types, an object where an array was expected) — `api()` turns that into
// an ApiError(502). They stay LENIENT about enum values: `state`/`status` are
// passed through untouched because the UI already renders unknown ones safely;
// `layout`/`structure_mode` (no UI fallback) are coerced to a known default.
// ---------------------------------------------------------------------------

type Raw = Record<string, unknown>;
const isObj = (v: unknown): v is Raw =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const obj = (v: unknown, field: string): Raw => {
  if (!isObj(v)) throw new Error(`${field}: expected object`);
  return v;
};
const str = (v: unknown, field: string): string => {
  if (typeof v !== "string") throw new Error(`${field}: expected string`);
  return v;
};
const arr = (v: unknown, field: string): unknown[] => {
  if (!Array.isArray(v)) throw new Error(`${field}: expected array`);
  return v;
};
const optStr = (v: unknown): string | undefined =>
  typeof v === "string" ? v : undefined;
const optNum = (v: unknown): number | undefined =>
  typeof v === "number" && Number.isFinite(v) ? v : undefined;
const looseStr = (v: unknown, fallback = ""): string =>
  typeof v === "string" ? v : fallback;
const looseNum = (v: unknown, fallback = 0): number =>
  typeof v === "number" && Number.isFinite(v) ? v : fallback;
const looseBool = (v: unknown, fallback = false): boolean =>
  typeof v === "boolean" ? v : fallback;
const strArr = (v: unknown): string[] | undefined =>
  Array.isArray(v)
    ? v.filter((x): x is string => typeof x === "string")
    : undefined;
const pick = <T extends string>(
  v: unknown,
  allowed: readonly T[],
  fallback: T,
): T =>
  typeof v === "string" && (allowed as readonly string[]).includes(v)
    ? (v as T)
    : fallback;

// Validator for the auth-status shape. This response decides whether the whole
// app is shown, so a malformed one must be rejected (and treated as
// unauthenticated by the caller) rather than trusted.
function asAuthStatus(raw: unknown): AuthStatus {
  const o = obj(raw, "auth status");
  if (typeof o.enabled !== "boolean" || typeof o.authenticated !== "boolean") {
    throw new Error("auth status: missing enabled/authenticated");
  }
  return {
    enabled: o.enabled,
    authenticated: o.authenticated,
    username: optStr(o.username),
  };
}

function vLibrary(raw: unknown): Library {
  const o = obj(raw, "library");
  return {
    id: str(o.id, "library.id"),
    name: str(o.name, "library.name"),
    root_path: looseStr(o.root_path),
    structure_mode: pick(
      o.structure_mode,
      ["author_first", "series_first"] as const,
      "author_first",
    ),
    author_folder_mode: pick(
      o.author_folder_mode,
      ["sort", "name"] as const,
      "sort",
    ),
    file_template: looseStr(o.file_template),
    multi_file_template: looseStr(o.multi_file_template),
    enabled: looseBool(o.enabled, true),
    write_tags: looseBool(o.write_tags, false),
    embed_cover: looseBool(o.embed_cover, false),
    created_at: looseStr(o.created_at),
    updated_at: looseStr(o.updated_at),
  };
}
const vLibraries = (raw: unknown): Library[] =>
  arr(raw, "libraries").map(vLibrary);

function vBookFile(raw: unknown): BookFile {
  const o = obj(raw, "book file");
  return {
    id: str(o.id, "book_file.id"),
    rel_path: looseStr(o.rel_path),
    size: looseNum(o.size),
    mod_time: looseNum(o.mod_time),
    ext: looseStr(o.ext),
    track: optNum(o.track),
  };
}
function vBook(raw: unknown): Book {
  const o = obj(raw, "book");
  return {
    id: str(o.id, "book.id"),
    library_id: str(o.library_id, "book.library_id"),
    source_dir: looseStr(o.source_dir),
    source_file: optStr(o.source_file),
    layout: pick(o.layout, ["single", "multi"] as const, "single"),
    // `state` kept as-sent: the UI has a fallback for unknown states.
    state: (typeof o.state === "string"
      ? o.state
      : "unmatched") as Book["state"],
    message: optStr(o.message),
    match_score: optNum(o.match_score),
    matched_provider: optStr(o.matched_provider),
    title: optStr(o.title),
    subtitle: optStr(o.subtitle),
    author: optStr(o.author),
    author_sort: optStr(o.author_sort),
    author_sort_source: pick(
      o.author_sort_source,
      ["derived", "manual"] as const,
      "derived",
    ),
    narrator: optStr(o.narrator),
    series: optStr(o.series),
    series_index: optStr(o.series_index),
    year: optNum(o.year),
    asin: optStr(o.asin),
    isbn: optStr(o.isbn),
    cover_url: optStr(o.cover_url),
    files:
      o.files === undefined
        ? undefined
        : arr(o.files, "book.files").map(vBookFile),
    updated_at: looseStr(o.updated_at),
  };
}
function vBooksResponse(raw: unknown): BooksResponse {
  const o = obj(raw, "books response");
  const counts: Record<string, number> = {};
  if (isObj(o.counts)) {
    for (const [k, v] of Object.entries(o.counts))
      if (typeof v === "number") counts[k] = v;
  }
  return { books: arr(o.books, "books").map(vBook), counts };
}

function vAcceptOutcome(raw: unknown): AcceptOutcome {
  const o = obj(raw, "accept outcome");
  return {
    considered: looseNum(o.considered),
    accepted: looseNum(o.accepted),
    no_candidates: looseNum(o.no_candidates),
    below_score: looseNum(o.below_score),
  };
}

function vJob(raw: unknown): Job {
  const o = obj(raw, "job");
  return {
    id: str(o.id, "job.id"),
    // `type`/`status` kept as-sent: the four known `type`s and five known
    // `status`es all render safely, and so does an unrecognised token (the
    // Activity page just prints it). A non-string is the only thing coerced —
    // to a visibly-wrong `"unknown"` rather than silently to a real value.
    type: (typeof o.type === "string" ? o.type : "unknown") as Job["type"],
    status: (typeof o.status === "string"
      ? o.status
      : "queued") as Job["status"],
    library_id: optStr(o.library_id),
    total: looseNum(o.total),
    done: looseNum(o.done),
    message: optStr(o.message),
    error: optStr(o.error),
    created_at: looseStr(o.created_at),
    finished_at: optStr(o.finished_at),
  };
}
const vJobs = (raw: unknown): Job[] => arr(raw, "jobs").map(vJob);

function vCandidate(raw: unknown): Candidate {
  const o = obj(raw, "candidate");
  return {
    provider: str(o.provider, "candidate.provider"),
    provider_id: str(o.provider_id, "candidate.provider_id"),
    title: str(o.title, "candidate.title"),
    subtitle: optStr(o.subtitle),
    authors: strArr(o.authors),
    narrators: strArr(o.narrators),
    series: optStr(o.series),
    series_index: optStr(o.series_index),
    year: optNum(o.year),
    asin: optStr(o.asin),
    isbn: optStr(o.isbn),
    cover_url: optStr(o.cover_url),
    score: optNum(o.score),
  };
}
const vCandidates = (raw: unknown): Candidate[] =>
  arr(raw, "candidates").map(vCandidate);

function vMatchResponse(raw: unknown): MatchResponse {
  const o = obj(raw, "match response");
  return {
    book: vBook(o.book),
    candidates:
      o.candidates === undefined ? undefined : vCandidates(o.candidates),
  };
}

function vFileMove(raw: unknown): FileMove {
  const o = obj(raw, "file move");
  return {
    from_rel: looseStr(o.from_rel),
    to_rel: looseStr(o.to_rel),
    no_op: looseBool(o.no_op),
  };
}
function vTagFilePlan(raw: unknown): TagFilePlan {
  const o = obj(raw, "tag file plan");
  return {
    file_rel: looseStr(o.file_rel),
    writable: looseBool(o.writable),
    changed: looseBool(o.changed),
    reason: optStr(o.reason),
  };
}
function vBookPlan(raw: unknown): BookPlan {
  const o = obj(raw, "book plan");
  return {
    book_id: str(o.book_id, "book_plan.book_id"),
    title: looseStr(o.title),
    // A skipped book carries no moves; the server may send [] or null/absent.
    moves: Array.isArray(o.moves) ? o.moves.map(vFileMove) : [],
    skip: looseBool(o.skip),
    reason: optStr(o.reason),
    // Present only when the library has write_tags on; undefined (not []) is
    // how the UI tells "not applicable" from "applicable, nothing planned".
    tag_files: Array.isArray(o.tag_files) ? o.tag_files.map(vTagFilePlan) : undefined,
  };
}
function vOrganizePlan(raw: unknown): OrganizePlan {
  const o = obj(raw, "organize plan");
  return {
    library_id: looseStr(o.library_id),
    root_path: looseStr(o.root_path),
    // An empty preview (nothing selected / nothing matched) legitimately has
    // no books.
    books: Array.isArray(o.books) ? o.books.map(vBookPlan) : [],
    conflicts: strArr(o.conflicts),
  };
}

function vSettings(raw: unknown): Settings {
  const o = obj(raw, "settings");
  const audible = obj(o.audible, "settings.audible");
  const google = obj(o.google_books, "settings.google_books");
  const openLib = obj(o.open_library, "settings.open_library");
  const auth = obj(o.auth, "settings.auth");
  return {
    auto_match_threshold: looseNum(o.auto_match_threshold, 0.85),
    audible: {
      enabled: looseBool(audible.enabled),
      region: looseStr(audible.region, "us"),
    },
    google_books: {
      enabled: looseBool(google.enabled),
      api_key_set: looseBool(google.api_key_set),
    },
    open_library: { enabled: looseBool(openLib.enabled) },
    auth: {
      enabled: looseBool(auth.enabled),
      username: optStr(auth.username),
      api_key_set: looseBool(auth.api_key_set),
      api_key: optStr(auth.api_key),
    },
  };
}

export interface DirEntry {
  name: string;
  path: string;
}

/** One folder listing from GET /browse, used by the library root picker. */
export interface DirListing {
  /** The folder listed; "" for the root listing (drives, or "/"). */
  path: string;
  /** Folder to go up to; "" when `path` is already a filesystem root. */
  parent: string;
  entries: DirEntry[];
  truncated?: boolean;
}

function vDirListing(raw: unknown): DirListing {
  const o = obj(raw, "dir listing");
  return {
    path: looseStr(o.path),
    parent: looseStr(o.parent),
    entries: arr(o.entries, "dir_listing.entries").map((e, i) => {
      const d = obj(e, `dir_listing.entries[${i}]`);
      return {
        name: str(d.name, `dir_listing.entries[${i}].name`),
        path: str(d.path, `dir_listing.entries[${i}].path`),
      };
    }),
    truncated: looseBool(o.truncated),
  };
}

export interface AuthStatus {
  enabled: boolean;
  authenticated: boolean;
  username?: string;
}

/** Options accepted by the read-only endpoints that useAsync drives. */
export interface ReadOpts {
  signal?: AbortSignal;
}

export const client = {
  // Not consumed structurally — left untyped-at-runtime on purpose.
  health: () => api<{ status: string; time: string }>("/healthz"),
  logout: () => api<{ ok: boolean }>("/auth/logout", { method: "POST" }),

  authStatus: (opts?: ReadOpts) =>
    api<AuthStatus>("/auth/status", { signal: opts?.signal }, asAuthStatus),
  login: (username: string, password: string) =>
    api<AuthStatus>(
      "/auth/login",
      { method: "POST", body: JSON.stringify({ username, password }) },
      asAuthStatus,
    ),

  /** List the folders inside `path`; pass "" for the filesystem roots. */
  browse: (path: string, opts?: ReadOpts) =>
    api<DirListing>(
      `/browse?path=${encodeURIComponent(path)}`,
      { signal: opts?.signal },
      vDirListing,
    ),

  listLibraries: (opts?: ReadOpts) =>
    api<Library[]>("/libraries", { signal: opts?.signal }, vLibraries),
  createLibrary: (input: Partial<Library>) =>
    api<Library>(
      "/libraries",
      { method: "POST", body: JSON.stringify(input) },
      vLibrary,
    ),
  updateLibrary: (id: string, input: Partial<Library>) =>
    api<Library>(
      `/libraries/${id}`,
      { method: "PATCH", body: JSON.stringify(input) },
      vLibrary,
    ),
  deleteLibrary: (id: string) =>
    api<void>(`/libraries/${id}`, { method: "DELETE" }),
  scanLibrary: (id: string) =>
    api<Job>(`/libraries/${id}/scan`, { method: "POST" }, vJob),
  // Enqueue a scan for every enabled library in one call.
  rescanAllLibraries: () =>
    api<{ jobs: Job[] }>(
      `/libraries/rescan-all`,
      { method: "POST" },
      (raw) => ({ jobs: vJobs((raw as { jobs: unknown }).jobs) }),
    ),

  listBooks: (
    params: { library_id?: string; state?: string; q?: string } = {},
    opts?: ReadOpts,
  ) => {
    const qs = new URLSearchParams(
      Object.entries(params).filter(([, v]) => v) as [string, string][],
    ).toString();
    return api<BooksResponse>(
      `/books${qs ? `?${qs}` : ""}`,
      { signal: opts?.signal },
      vBooksResponse,
    );
  },
  getBook: (id: string, opts?: ReadOpts) =>
    api<Book>(`/books/${id}`, { signal: opts?.signal }, vBook),
  // Delete one book: its audio files are removed from disk and its row from the
  // database. Not undoable.
  deleteBook: (id: string) => api<void>(`/books/${id}`, { method: "DELETE" }),
  // Delete several books at once; see DeleteBooksResult for the partial-run shape.
  deleteBooks: (ids: string[]) =>
    api<DeleteBooksResult>(`/books/delete`, {
      method: "POST",
      body: JSON.stringify({ ids }),
    }),
  updateBook: (id: string, patch: { author_sort?: string }) =>
    api<Book>(
      `/books/${id}`,
      { method: "PATCH", body: JSON.stringify(patch) },
      vBook,
    ),
  listCandidates: (id: string, opts?: ReadOpts) =>
    api<Candidate[]>(
      `/books/${id}/candidates`,
      { signal: opts?.signal },
      vCandidates,
    ),
  autoMatch: (id: string) =>
    api<MatchResponse>(
      `/books/${id}/match`,
      { method: "POST", body: JSON.stringify({ auto: true }) },
      vMatchResponse,
    ),
  acceptCandidate: (id: string, provider: string, provider_id: string) =>
    api<MatchResponse>(
      `/books/${id}/match`,
      { method: "POST", body: JSON.stringify({ provider, provider_id }) },
      vMatchResponse,
    ),
  acceptManual: (id: string, manual: ManualMatch) =>
    api<MatchResponse>(
      `/books/${id}/match`,
      { method: "POST", body: JSON.stringify({ manual }) },
      vMatchResponse,
    ),
  searchMetadata: (
    body: {
      q?: string;
      title?: string;
      author?: string;
      year?: number;
      provider?: string;
    },
    opts?: ReadOpts,
  ) =>
    api<Candidate[]>(
      `/search`,
      { method: "POST", body: JSON.stringify(body), signal: opts?.signal },
      vCandidates,
    ),
  // Accept the top stored candidate for every unmatched / needs-review book
  // scoring at least min_score. An empty library_id sweeps every library.
  acceptTopCandidates: (library_id: string, min_score: number) =>
    api<AcceptOutcome>(
      `/books/accept-top`,
      { method: "POST", body: JSON.stringify({ library_id, min_score }) },
      vAcceptOutcome,
    ),
  matchLibrary: (id: string) =>
    api<Job>(`/libraries/${id}/match`, { method: "POST" }, vJob),

  organizePreview: (library_id: string, book_ids?: string[]) =>
    api<OrganizePlan>(
      `/organize/preview`,
      { method: "POST", body: JSON.stringify({ library_id, book_ids }) },
      vOrganizePlan,
    ),
  organizeApply: (library_id: string, book_ids?: string[]) =>
    api<Job>(
      `/organize/apply`,
      { method: "POST", body: JSON.stringify({ library_id, book_ids }) },
      vJob,
    ),

  listJobs: (limit = 100, opts?: ReadOpts) =>
    api<Job[]>(`/jobs?limit=${limit}`, { signal: opts?.signal }, vJobs),
  getJob: (id: string, opts?: ReadOpts) =>
    api<Job>(`/jobs/${id}`, { signal: opts?.signal }, vJob),
  cancelJob: (id: string) =>
    api<void>(`/jobs/${id}/cancel`, { method: "POST" }),
  undoJob: (id: string) =>
    api<Job>(`/jobs/${id}/undo`, { method: "POST" }, vJob),

  getSettings: (opts?: ReadOpts) =>
    api<Settings>("/settings", { signal: opts?.signal }, vSettings),
  patchSettings: (patch: Record<string, unknown>) =>
    api<Settings>(
      "/settings",
      { method: "PATCH", body: JSON.stringify(patch) },
      vSettings,
    ),
};
