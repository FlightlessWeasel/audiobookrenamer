-- Speeds up CurrentTagBackupOwner, which looks up the most recent tag-write
-- journal row for a given target path (op='tagwrite') to decide whether an
-- older job's backup for that path is still the one in force, or has since
-- been superseded by a later tag-write.
CREATE INDEX idx_rename_ops_tagwrite_dst ON rename_ops(op, dst);
