-- A book is identified by its folder, or — when standalone audio files sit
-- loose beside their siblings (e.g. one .m4b per book in an author folder) —
-- by the file path. source_file is '' for folder-based books.

DROP INDEX idx_books_source;

ALTER TABLE books ADD COLUMN source_file      TEXT NOT NULL DEFAULT '';
ALTER TABLE books ADD COLUMN scan_fingerprint TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_books_identity ON books(library_id, source_dir, source_file);
