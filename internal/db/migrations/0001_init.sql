-- Initial schema.

CREATE TABLE libraries (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    root_path           TEXT NOT NULL,
    structure_mode      TEXT NOT NULL DEFAULT 'author_first',
    file_template       TEXT NOT NULL DEFAULT '{title} ({year}) - {author}{ext}',
    multi_file_template TEXT NOT NULL DEFAULT '{title} ({year}) - {track2}{ext}',
    enabled             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_libraries_root ON libraries(root_path);

CREATE TABLE books (
    id                  TEXT PRIMARY KEY,
    library_id          TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    source_dir          TEXT NOT NULL,
    layout              TEXT NOT NULL,
    state               TEXT NOT NULL DEFAULT 'unmatched',
    message             TEXT NOT NULL DEFAULT '',

    matched_provider    TEXT NOT NULL DEFAULT '',
    matched_provider_id TEXT NOT NULL DEFAULT '',
    match_score         REAL NOT NULL DEFAULT 0,

    title               TEXT NOT NULL DEFAULT '',
    subtitle            TEXT NOT NULL DEFAULT '',
    author              TEXT NOT NULL DEFAULT '',
    author_sort         TEXT NOT NULL DEFAULT '',
    narrator            TEXT NOT NULL DEFAULT '',
    series              TEXT NOT NULL DEFAULT '',
    series_index        TEXT NOT NULL DEFAULT '',
    year                INTEGER NOT NULL DEFAULT 0,
    asin                TEXT NOT NULL DEFAULT '',
    isbn                TEXT NOT NULL DEFAULT '',
    cover_url           TEXT NOT NULL DEFAULT '',

    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_books_source ON books(library_id, source_dir);
CREATE INDEX idx_books_state ON books(state);

CREATE TABLE book_files (
    id       TEXT PRIMARY KEY,
    book_id  TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    size     INTEGER NOT NULL DEFAULT 0,
    mod_time INTEGER NOT NULL DEFAULT 0,
    ext      TEXT NOT NULL DEFAULT '',
    track    INTEGER NOT NULL DEFAULT 0,
    tag_json TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX idx_book_files_path ON book_files(book_id, rel_path);

CREATE TABLE candidates (
    id           TEXT PRIMARY KEY,
    book_id      TEXT NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    provider     TEXT NOT NULL,
    provider_id  TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    score        REAL NOT NULL DEFAULT 0,
    rank         INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_candidates_book ON candidates(book_id);

CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    type        TEXT NOT NULL,
    status      TEXT NOT NULL,
    library_id  TEXT NOT NULL DEFAULT '',
    total       INTEGER NOT NULL DEFAULT 0,
    done        INTEGER NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    finished_at TEXT
);

CREATE INDEX idx_jobs_created ON jobs(created_at DESC);

CREATE TABLE rename_ops (
    id      TEXT PRIMARY KEY,
    job_id  TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    seq     INTEGER NOT NULL,
    op      TEXT NOT NULL,            -- mkdir | move | casefix
    src     TEXT NOT NULL DEFAULT '',
    dst     TEXT NOT NULL DEFAULT '',
    status  TEXT NOT NULL DEFAULT 'pending', -- pending | done | failed | reverted
    error   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_rename_ops_job ON rename_ops(job_id, seq);

CREATE TABLE provider_cache (
    key        TEXT PRIMARY KEY,
    provider   TEXT NOT NULL,
    body_json  TEXT NOT NULL,
    fetched_at TEXT NOT NULL
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
