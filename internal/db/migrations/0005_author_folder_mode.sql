-- A library files books under the author's sort name ("Campbell, Jack") by
-- default. 'name' files them under the display name ("Jack Campbell")
-- instead. Existing libraries keep the behaviour they already had.
ALTER TABLE libraries ADD COLUMN author_folder_mode TEXT NOT NULL DEFAULT 'sort';
