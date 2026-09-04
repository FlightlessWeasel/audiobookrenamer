-- Cached cover-art bytes for a matched book, fetched once from the accepted
-- candidate's cover_url so organize can embed a cover without touching the
-- network. A row's presence implies it is fresh for the book's current
-- cover_url: the matcher deletes it the moment cover_url changes to anything
-- else, so a stale image is never attributed to the wrong book.
CREATE TABLE book_covers (
    book_id    TEXT PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
    mime       TEXT NOT NULL,
    data       BLOB NOT NULL,
    source_url TEXT NOT NULL,
    fetched_at TEXT NOT NULL
);
