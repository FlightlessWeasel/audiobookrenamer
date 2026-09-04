package organize

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/tagwrite"

	"github.com/google/uuid"
)

// executeTagWrite journals, backs up, and rewrites the embedded tags of one
// file at its final (post-move) path, returning the completedStep rollback
// needs to reverse it.
//
// Every tagwrite.Writer replaces its target atomically (temp file + rename),
// so a failure here never leaves target partially written — it is either
// still exactly what it was, or exactly the new tags. The only possible
// leftover from a failure is an unused backup copy, which the next tag-write
// for this path reuses or overwrites (see reuseOrAllocBackupPath).
func executeTagWrite(database *db.DB, jobID string, seq int, target string, desired tagwrite.TagSet, backupDir string) (completedStep, error) {
	w, err := tagwrite.WriterFor(filepath.Ext(target))
	if err != nil {
		return completedStep{}, fmt.Errorf("tagwrite %s: %w", target, err)
	}

	backupPath, err := reuseOrAllocBackupPath(database, target, backupDir)
	if err != nil {
		return completedStep{}, fmt.Errorf("tag backup path for %s: %w", target, err)
	}

	// The journal row must be durable before the file is touched, exactly like
	// every other step Execute performs.
	id, err := database.InsertRenameOp(jobID, seq, string(OpTagWrite), backupPath, target)
	if err != nil {
		return completedStep{}, fmt.Errorf("journal tagwrite %s: %w", target, err)
	}
	failStep := func(cause error) (completedStep, error) {
		if mErr := database.MarkRenameOp(id, "failed", cause.Error()); mErr != nil {
			slog.Warn("could not journal tagwrite step failure", "op", id, "cause", cause, "err", mErr)
		}
		return completedStep{}, cause
	}

	if err := backupFile(target, backupPath); err != nil {
		return failStep(fmt.Errorf("back up %s before rewriting tags: %w", target, err))
	}
	if err := w.Write(target, desired); err != nil {
		return failStep(fmt.Errorf("write tags to %s: %w", target, err))
	}
	// Re-read what actually landed on disk and compare against what was
	// requested. This is the executor's own last line of defence against a
	// writer bug that builds well-formed but wrong bytes: a mismatch aborts
	// (and rolls back) rather than silently shipping the wrong tags.
	got, err := w.Read(target)
	if err != nil {
		return failStep(fmt.Errorf("re-read %s after writing tags: %w", target, err))
	}
	if !got.Equal(desired) {
		return failStep(fmt.Errorf("tags written to %s do not match what was planned", target))
	}
	if err := database.MarkRenameOp(id, "done", ""); err != nil {
		return completedStep{}, fmt.Errorf("journal update for tagwrite %s: %w", target, err)
	}
	return completedStep{opID: id, kind: string(OpTagWrite), src: backupPath, dst: target}, nil
}

// reuseOrAllocBackupPath returns the backup file target's next tag-write
// should use: the existing backup file for target if one is still owned by a
// completed tag-write (see db.CurrentTagBackupOwner) and still present on
// disk, or a freshly allocated path in backupDir otherwise. Reusing the same
// file is what keeps only one tag-write backup in existence per path.
func reuseOrAllocBackupPath(database *db.DB, target, backupDir string) (string, error) {
	if _, prev, ok, err := database.CurrentTagBackupOwner(target); err != nil {
		return "", err
	} else if ok {
		if _, statErr := os.Stat(prev); statErr == nil {
			return prev, nil
		}
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create tag backup dir %s: %w", backupDir, err)
	}
	return filepath.Join(backupDir, uuid.NewString()+filepath.Ext(target)), nil
}

// restoreTagFile restores path from the tag-write backup at backupPath, but
// only if opID is still that backup's current owner (see
// db.CurrentTagBackupOwner) — i.e. no later tag-write for the same path has
// since reused and overwritten it. isOwner reports which case applied; the
// caller treats isOwner==false the way it treats a retained non-empty folder:
// not an error, but worth telling the user about. It does not remove the
// backup file or update the journal row — the caller does both once it has
// durably recorded the reversal, so a crash between this call and that record
// leaves a retryable, idempotent state (the backup is still there to copy from
// again) rather than a gap.
//
// isOwner can also come back false even when opID is nominally still the
// current owner in the database: reverting a *later* tag-write op (a separate
// undo, of a job that reused this same backup file and then, on success,
// deleted it) hands ownership back to this one without the file being there
// any more. That is the same "nothing left to restore" outcome as a proper
// supersession, so it is reported the same way rather than as a failure.
func restoreTagFile(database *db.DB, opID, backupPath, path string) (isOwner bool, err error) {
	ownerID, _, ok, err := database.CurrentTagBackupOwner(path)
	if err != nil {
		return false, err
	}
	if !ok || ownerID != opID {
		return false, nil
	}
	if _, statErr := os.Stat(backupPath); statErr != nil {
		return false, nil
	}
	if err := backupFile(backupPath, path); err != nil {
		return true, fmt.Errorf("restore tags for %s: %w", path, err)
	}
	return true, nil
}

// backupFile copies src's current bytes to dst, replacing it, via an fsynced
// temp file in dst's directory before the rename that makes it visible. src
// and dst are frequently on different filesystems (the library root and the
// backup directory, typically), so this is always a copy — there is no
// same-filesystem rename fast path to attempt, unlike a plan move.
func backupFile(src, dst string) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".abrtagbak-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("place %s: %w", dst, err)
	}
	return nil
}
