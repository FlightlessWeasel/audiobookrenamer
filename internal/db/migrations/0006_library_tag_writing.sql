-- Per-library opt-in for rewriting the embedded tags of the audio files
-- themselves during organize. Both default off: every other organize step only
-- moves and renames files, so mutating their contents must be chosen
-- deliberately. embed_cover is a sub-option of write_tags — it has no effect
-- while write_tags is off.
ALTER TABLE libraries ADD COLUMN write_tags  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE libraries ADD COLUMN embed_cover INTEGER NOT NULL DEFAULT 0;
