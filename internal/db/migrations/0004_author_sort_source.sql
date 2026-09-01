-- author_sort is either DERIVED from the author name ("First Last" -> "Last,
-- First") or set by hand. Track which, so a later match can refresh a derived
-- value without ever clobbering a manual override.
ALTER TABLE books ADD COLUMN author_sort_source TEXT NOT NULL DEFAULT 'derived';
