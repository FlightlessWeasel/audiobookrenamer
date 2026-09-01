-- Organize (and undo) jobs need to carry the set of books they act on.
ALTER TABLE jobs ADD COLUMN payload TEXT NOT NULL DEFAULT '';
