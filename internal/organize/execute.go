package organize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/google/uuid"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/pathguard"
)

// bookSnapshot is stored (as a journal row) before a book is moved so undo can
// restore the database rows exactly.
type bookSnapshot struct {
	BookID     string            `json:"book_id"`
	SourceDir  string            `json:"source_dir"`
	SourceFile string            `json:"source_file"`
	State      model.BookState   `json:"state"`
	FileRel    map[string]string `json:"file_rel"` // fileID -> rel_path
}

// ProgressFunc reports execution progress (done of total, with a message).
type ProgressFunc func(done, total int, msg string)

// Execute applies plan under jobID, journaling every filesystem step. On the
// first failure it rolls back all completed steps and returns the error; no
// database rows are changed unless the whole plan succeeds.
func Execute(ctx context.Context, database *db.DB, jobID string, plan *Plan, progress ProgressFunc) error {
	if progress == nil {
		progress = func(int, int, string) {}
	}

	var mkdirs []string
	var moves []moveStep
	rmdirSet := map[string]struct{}{}

	active := make([]*BookPlan, 0, len(plan.Books))
	for i := range plan.Books {
		bp := &plan.Books[i]
		if bp.Skip {
			continue
		}
		active = append(active, bp)

		for _, dir := range ancestorDirs(plan.RootPath, bp.NewSourceDir) {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				mkdirs = append(mkdirs, dir)
			}
		}
		for _, m := range bp.Moves {
			if m.NoOp {
				continue
			}
			src := filepath.Join(plan.RootPath, filepath.FromSlash(m.FromRel))
			dst := filepath.Join(plan.RootPath, filepath.FromSlash(m.ToRel))
			kind := string(OpMove)
			if EqualFold(src, dst) {
				kind = string(OpCaseFix)
			}
			moves = append(moves, moveStep{kind, src, dst})
		}
		// After the moves the book's old location may be empty. Enqueue its
		// directory chain for pruning; os.Remove only succeeds on empty dirs,
		// so a shared folder with other content is left untouched.
		oldDir := bp.OldSourceDir
		if bp.OldSourceFile != "" {
			oldDir = filepath.Dir(bp.OldSourceFile)
		}
		if filepath.Clean(oldDir) != filepath.Clean(bp.NewSourceDir) {
			for _, dir := range emptyingDirs(plan.RootPath, oldDir, bp.NewSourceDir) {
				rmdirSet[dir] = struct{}{}
			}
		}
	}

	if len(active) == 0 {
		return fmt.Errorf("nothing to do: all books skipped")
	}

	// Containment guard: every move must resolve to a location inside the
	// library root (following any symlinked parent directories), and we refuse
	// to rename a symlink itself. This is the executor's own last line of
	// defence in addition to the checks in the planner. The same check is
	// re-run per move immediately before its os.Rename (checkMoveContainment)
	// so a symlink swapped in after this batch pass can't be used to escape.
	for _, m := range moves {
		if err := checkMoveContainment(plan.RootPath, m.src, m.dst); err != nil {
			return err
		}
	}

	mkdirs = uniqueSorted(mkdirs, false) // shallow -> deep
	rmdirs := make([]string, 0, len(rmdirSet))
	for d := range rmdirSet {
		rmdirs = append(rmdirs, d)
	}
	rmdirs = uniqueSorted(rmdirs, true) // deep -> shallow

	tagOps := 0
	for _, bp := range active {
		for _, tf := range bp.TagFiles {
			if tf.Writable && tf.Changed {
				tagOps++
			}
		}
	}

	total := len(mkdirs) + len(moves) + len(rmdirs) + tagOps
	seq := 0
	done := 0

	var completed []completedStep // for rollback, replayed in reverse
	fail := func(err error) error {
		if rbErrs := rollback(database, completed); len(rbErrs) > 0 {
			joined := errors.Join(rbErrs...)
			slog.Error("organize rollback incomplete", "job", jobID, "cause", err, "rollback_errors", joined)
			return fmt.Errorf("%w; rollback also failed: %v", err, joined)
		}
		return err
	}

	// markDone records a journal step as completed; a failure here is fatal
	// because undo keys off the "done" status to know which steps to reverse.
	markDone := func(id, what string) error {
		if err := database.MarkRenameOp(id, "done", ""); err != nil {
			return fmt.Errorf("journal update for %s: %w", what, err)
		}
		return nil
	}

	// markFailed records a step as failed. It is deliberately NOT fatal on the
	// paths that call it: every caller returns fail(err) immediately afterwards
	// and rollback reverses whatever physical work already completed, so a
	// journal row left non-"done" there is the correct end state (undo only
	// reverses "done" steps). A write failure is therefore only logged.
	markFailed := func(id, cause string) {
		if err := database.MarkRenameOp(id, "failed", cause); err != nil {
			slog.Warn("could not journal step failure", "job", jobID, "op", id, "cause", cause, "err", err)
		}
	}

	for _, dir := range mkdirs {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		// The journal row must be durably written before the filesystem
		// change: without it the step cannot be undone, so a journal-write
		// failure aborts the run (rolling back anything already done).
		id, err := database.InsertRenameOp(jobID, seq, string(OpMkdir), "", dir)
		if err != nil {
			return fail(fmt.Errorf("journal mkdir %s: %w", dir, err))
		}
		seq++
		if err := os.MkdirAll(dir, 0o755); err != nil {
			markFailed(id, err.Error())
			return fail(fmt.Errorf("mkdir %s: %w", dir, err))
		}
		// Record the completed step before flipping the journal row to "done":
		// the filesystem change has happened, so a failure to update the
		// journal must still leave this step in `completed` for rollback.
		completed = append(completed, completedStep{opID: id, kind: string(OpMkdir), dst: dir})
		if err := markDone(id, "mkdir "+dir); err != nil {
			return fail(err)
		}
		done++
		progress(done, total, "creating folders")
	}

	for _, m := range moves {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		if m.kind == string(OpCaseFix) {
			// A case-only rename goes through a uniquely-named temp sibling.
			// Both legs are journaled as plain moves so a crash mid-rename (or
			// an undo) has a complete, replayable record and the temp name is
			// never guessed or raced.
			tmp := m.src + ".abrtmp-" + uuid.NewString()
			id1, err := database.InsertRenameOp(jobID, seq, string(OpMove), m.src, tmp)
			if err != nil {
				return fail(fmt.Errorf("journal case-fix %s: %w", m.src, err))
			}
			seq++
			// TOCTOU re-check: the up-front pass ran before any rename; verify
			// this endpoint pair is still contained (and src still not a
			// symlink) right before we touch it.
			if err := checkMoveContainment(plan.RootPath, m.src, tmp); err != nil {
				markFailed(id1, err.Error())
				return fail(err)
			}
			if err := os.Rename(m.src, tmp); err != nil {
				markFailed(id1, err.Error())
				return fail(fmt.Errorf("case-fix %s: %w", m.src, err))
			}
			// Physical change done -> in `completed` before touching the journal.
			completed = append(completed, completedStep{opID: id1, kind: string(OpMove), src: m.src, dst: tmp})
			if err := markDone(id1, "case-fix "+m.src); err != nil {
				return fail(err)
			}

			id2, err := database.InsertRenameOp(jobID, seq, string(OpMove), tmp, m.dst)
			if err != nil {
				return fail(fmt.Errorf("journal case-fix %s: %w", m.dst, err))
			}
			seq++
			// Leg 1 emptied the source path, so anything now resolving at m.dst
			// is a genuinely foreign file (on a case-insensitive filesystem
			// m.dst and m.src name the same location, which is now empty).
			// os.Rename would overwrite it on Unix -> refuse and roll back.
			if _, err := os.Lstat(m.dst); err == nil {
				return fail(fmt.Errorf("case-fix %s -> %s: destination already exists", tmp, m.dst))
			}
			// tmp is ours, but re-verify it still resolves inside root before
			// the second leg (the symlink check on tmp is harmless).
			if err := checkMoveContainment(plan.RootPath, tmp, m.dst); err != nil {
				markFailed(id2, err.Error())
				return fail(err)
			}
			if err := os.Rename(tmp, m.dst); err != nil {
				markFailed(id2, err.Error())
				return fail(fmt.Errorf("case-fix %s -> %s: %w", tmp, m.dst, err))
			}
			completed = append(completed, completedStep{opID: id2, kind: string(OpMove), src: tmp, dst: m.dst})
			if err := markDone(id2, "case-fix "+m.dst); err != nil {
				return fail(err)
			}
			done++
			progress(done, total, filepath.Base(m.dst))
			continue
		}

		id, err := database.InsertRenameOp(jobID, seq, m.kind, m.src, m.dst)
		if err != nil {
			return fail(fmt.Errorf("journal move %s -> %s: %w", m.src, m.dst, err))
		}
		seq++
		// TOCTOU re-check immediately before the real rename (see the case-fix
		// legs above).
		if err := checkMoveContainment(plan.RootPath, m.src, m.dst); err != nil {
			markFailed(id, err.Error())
			return fail(err)
		}
		if err := doMove(m.kind, m.src, m.dst); err != nil {
			markFailed(id, err.Error())
			return fail(fmt.Errorf("move %s -> %s: %w", m.src, m.dst, err))
		}
		// Physical change done -> in `completed` before touching the journal.
		completed = append(completed, completedStep{opID: id, kind: m.kind, src: m.src, dst: m.dst})
		if err := markDone(id, "move "+m.dst); err != nil {
			return fail(err)
		}
		done++
		progress(done, total, filepath.Base(m.dst))
	}

	for _, dir := range rmdirs {
		id, err := database.InsertRenameOp(jobID, seq, string(OpRmdir), "", dir)
		if err != nil {
			return fail(fmt.Errorf("journal rmdir %s: %w", dir, err))
		}
		seq++
		if err := os.Remove(dir); err != nil {
			// A non-empty leftover dir is not fatal; note it and move on.
			markFailed(id, err.Error())
		} else {
			// The dir is gone -> record it for rollback before flipping the
			// journal row to "done". Undo only reverses steps whose status is
			// "done" (and rollback already recreates an OpRmdir dir), so a
			// failed journal update here would make undo silently skip
			// recreating this folder. Treat it exactly like mkdir/move: abort
			// and roll back.
			completed = append(completed, completedStep{opID: id, kind: string(OpRmdir), dst: dir})
			if err := markDone(id, "rmdir "+dir); err != nil {
				return fail(err)
			}
		}
		done++
		progress(done, total, "tidying")
	}

	// Rewrite embedded tags for any file planning found changed, now that
	// every book sits at its final path. This runs after every move and
	// before the snapshots/FinalizeOrganize below, so a tag-write failure
	// rolls back the moves too rather than leaving the library renamed but
	// only partially retagged.
	for _, bp := range active {
		for _, tf := range bp.TagFiles {
			if !tf.Writable || !tf.Changed {
				continue
			}
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			rel, ok := bp.NewFileRel[tf.FileID]
			if !ok {
				return fail(fmt.Errorf("tagwrite: %s: file missing from its own planned layout", tf.FileRel))
			}
			// NewFileRel is relative to plan.RootPath (the same as a move's
			// ToRel) until FinalizeOrganize rebases it to be relative to
			// NewSourceDir for storage in book_files.rel_path.
			target := filepath.Join(plan.RootPath, filepath.FromSlash(rel))
			step, err := executeTagWrite(database, jobID, seq, target, tf.desired, plan.BackupDir)
			if err != nil {
				return fail(err)
			}
			seq++
			completed = append(completed, step)
			done++
			progress(done, total, filepath.Base(target))
		}
	}

	// All filesystem steps succeeded — record the snapshots and the new book
	// locations atomically. If this fails, the filesystem moves are rolled
	// back so disk and database stay in agreement.
	finals := make([]db.OrganizeFinal, 0, len(active))
	for _, bp := range active {
		// Restore the book's real pre-run state on undo. A book re-organized
		// while already "organized" must go back to "organized", not "matched".
		// A hand-built plan with no recorded state falls back to the historical
		// default.
		priorState := bp.OldState
		if priorState == "" {
			priorState = model.StateMatched
		}
		snap := bookSnapshot{
			BookID: bp.BookID, SourceDir: bp.OldSourceDir, SourceFile: bp.OldSourceFile,
			State: priorState, FileRel: bp.OldFileRel,
		}
		raw, _ := json.Marshal(snap)
		finals = append(finals, db.OrganizeFinal{
			BookID:       bp.BookID,
			NewSourceDir: bp.NewSourceDir,
			Snapshot:     string(raw),
			FileRelByID:  rebase(bp.NewFileRel, bp.NewSourceDir, plan.RootPath),
		})
	}
	if err := database.FinalizeOrganize(jobID, seq, finals); err != nil {
		return fail(fmt.Errorf("record organize result: %w", err))
	}
	return nil
}

// Undo reverses a completed organize job. It runs in two passes: first every
// filesystem step is reversed (newest first), and only if that fully succeeds
// are the book rows restored. That ordering means a failure partway through
// leaves the database still describing the organized layout that is (mostly)
// still on disk, rather than pointing at an original location the files no
// longer occupy.
//
// Undo is idempotent: every step it reverses is flipped to "reverted" in the
// journal, already-reverted steps are skipped, and a call with nothing left to
// do returns nil without touching the filesystem. A failed journal update
// aborts the undo immediately rather than leaving a reversed step still marked
// "done"; a partially-failed undo can therefore be retried and it re-lists the
// ops and resumes where it stopped.
//
// UndoResult reports the parts of a reversal that could not be fully applied.
// Neither field is an error: both describe a deliberate, narrow trade-off
// (never delete user data; never restore tags from a backup a later run has
// since reused) rather than a failure of the undo itself.
type UndoResult struct {
	// RetainedDirs lists folders undo could not remove during mkdir-reversal
	// because the user had added files to them; those folders are kept (their
	// contents are never deleted).
	RetainedDirs []string
	// UnrestoredTagFiles lists files whose tag-write backup had already been
	// reused by a later tag-write (see db.CurrentTagBackupOwner), so this
	// undo left their current tags in place instead of restoring the older
	// ones it had recorded.
	UnrestoredTagFiles []string
}

// Undo reverses a completed organize job. It runs in two passes: first every
// filesystem step is reversed (newest first), and only if that fully succeeds
// are the book rows restored. That ordering means a failure partway through
// leaves the database still describing the organized layout that is (mostly)
// still on disk, rather than pointing at an original location the files no
// longer occupy.
//
// Undo is idempotent: every step it reverses is flipped to "reverted" in the
// journal, already-reverted steps are skipped, and a call with nothing left to
// do returns nil without touching the filesystem. A failed journal update
// aborts the undo immediately rather than leaving a reversed step still marked
// "done"; a partially-failed undo can therefore be retried and it re-lists the
// ops and resumes where it stopped.
func Undo(ctx context.Context, database *db.DB, jobID string) (UndoResult, error) {
	var res UndoResult

	ops, err := database.ListRenameOps(jobID)
	if err != nil {
		return res, err
	}

	// Nothing left to reverse? Then a repeat call is a no-op.
	if !undoPending(ops) {
		return res, nil
	}

	// markReverted flips a step to "reverted" once its filesystem effect has
	// been undone. The write is mandatory: if it fails the journal would still
	// call the step "done" while disk no longer reflects it, so the caller must
	// abort (a later retry re-lists the ops and resumes).
	markReverted := func(id string) error {
		if err := database.MarkRenameOp(id, "reverted", ""); err != nil {
			return fmt.Errorf("undo: journal update for step %s: %w", id, err)
		}
		return nil
	}

	// Pass 1: filesystem, newest first. Only "done" steps are reversed, so a
	// re-run skips anything already "reverted".
	for i := len(ops) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		o := ops[i]
		if o.Status != "done" {
			continue
		}
		switch o.Kind {
		case string(OpMove), string(OpCaseFix):
			// reverseMove, not doMove: a step still marked "done" whose file is
			// already back at o.Src is one whose reversal landed on disk but not
			// in the journal (a rollback that could not write, or an undo
			// interrupted between the two). Treat it as already reversed and
			// carry on rather than aborting the entire undo on it.
			if err := reverseMove(o.Src, o.Dst); err != nil {
				return res, fmt.Errorf("undo move %s -> %s: %w", o.Dst, o.Src, err)
			}
			if err := markReverted(o.ID); err != nil {
				return res, err
			}
		case string(OpMkdir):
			// Best-effort: os.Remove only succeeds on an empty dir. If the
			// user dropped files into a folder this organize run created, the
			// folder is left in place — deleting it would take their data with
			// it. It is reported via RetainedDirs instead.
			// TODO: only the folder path is reported, not an inventory of the
			// files kept inside it; there is no per-file "retained" report.
			if rmErr := os.Remove(o.Dst); rmErr != nil && !os.IsNotExist(rmErr) {
				res.RetainedDirs = append(res.RetainedDirs, o.Dst)
				slog.Warn("undo kept a non-empty folder created by organize",
					"job", jobID, "dir", o.Dst, "err", rmErr)
			}
			if err := markReverted(o.ID); err != nil {
				return res, err
			}
		case string(OpRmdir):
			if err := os.MkdirAll(o.Dst, 0o755); err != nil {
				return res, fmt.Errorf("undo rmdir %s: %w", o.Dst, err)
			}
			if err := markReverted(o.ID); err != nil {
				return res, err
			}
		case string(OpTagWrite):
			isOwner, err := restoreTagFile(database, o.ID, o.Src, o.Dst)
			if err != nil {
				return res, fmt.Errorf("undo tagwrite %s: %w", o.Dst, err)
			}
			if !isOwner {
				res.UnrestoredTagFiles = append(res.UnrestoredTagFiles, o.Dst)
				slog.Warn("undo could not restore tags: backup was reused by a later tag-write",
					"job", jobID, "file", o.Dst)
			}
			if err := markReverted(o.ID); err != nil {
				return res, err
			}
			if isOwner {
				if rmErr := os.Remove(o.Src); rmErr != nil && !os.IsNotExist(rmErr) {
					slog.Warn("could not remove consumed tag backup", "path", o.Src, "err", rmErr)
				}
			}
		}
	}

	// Pass 2: database — only reached when every filesystem step reversed.
	for i := len(ops) - 1; i >= 0; i-- {
		o := ops[i]
		if o.Kind != "snapshot" || o.Status == "reverted" {
			continue
		}
		var snap bookSnapshot
		if err := json.Unmarshal([]byte(o.Dst), &snap); err != nil {
			return res, err
		}
		if err := database.RestoreBookSnapshot(snap.BookID, snap.SourceDir, snap.SourceFile, snap.State, snap.FileRel); err != nil {
			return res, err
		}
		if err := markReverted(o.ID); err != nil {
			return res, err
		}
	}
	return res, nil
}

// undoPending reports whether a job still has any journal step to reverse: a
// "done" filesystem step, or a snapshot row not yet "reverted".
func undoPending(ops []db.RenameOp) bool {
	for _, o := range ops {
		if o.Kind == "snapshot" {
			if o.Status != "reverted" {
				return true
			}
			continue
		}
		if o.Status == "done" {
			return true
		}
	}
	return false
}

// doMove renames src to dst, refusing to overwrite an existing dst. Case-only
// renames are decomposed into two plain-move legs by the caller, so kind is
// informational here.
//
// A library root can span several filesystems — the common case is a Docker
// deployment that bind-mounts more than one volume under it — and os.Rename
// cannot move between them. Such a failure falls back to a copy-then-delete
// (moveAcrossDevices) rather than aborting the run.
func doMove(_ /* kind */, src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := renameFile(src, dst)
	if err != nil && renameNeedsCopyFallback(err) {
		return moveAcrossDevices(src, dst)
	}
	return err
}

// renameFile is os.Rename behind a seam so a test can simulate a filesystem
// that refuses an in-place rename.
var renameFile = os.Rename

// renameNeedsCopyFallback reports whether a failed rename should be retried as
// a copy-then-delete. Both cases mean "these two paths can't be linked in
// place, but the bytes can still be copied across":
//
//   - EXDEV: src and dst are on different filesystems — the classic case of a
//     library root spanning several bind mounts.
//   - EPERM: union and pooled filesystems (mergerfs, mhddfs, unionfs) and some
//     network mounts (CIFS, root-squashed NFS) reject a cross-directory rename
//     with "operation not permitted" instead of EXDEV.
//
// A genuine permission problem on the destination surfaces earlier as a failed
// MkdirAll, or here as EACCES (not EPERM), so it is not swallowed by this.
func renameNeedsCopyFallback(err error) bool {
	return isCrossDevice(err) || errors.Is(err, syscall.EPERM)
}

// reverseMove puts a file moved src -> dst back at src. It is idempotent: when
// dst is already gone and src is back in place the move has been reversed
// before (by a rollback whose journal write did not land, or by a retried undo)
// and there is nothing to do. Without that check the second attempt would fail
// on doMove's "destination already exists" guard and strand the whole reversal.
func reverseMove(src, dst string) error {
	if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Lstat(src); err == nil {
			return nil
		}
	}
	return doMove("", dst, src)
}

// moveAcrossDevices moves a file between filesystems, where os.Rename cannot.
// The copy lands in a temp file in the destination directory and is fsynced
// before being renamed into place, so an interrupted copy can never leave a
// truncated file at dst; src is removed only once dst is durable. Mode and
// modification time are preserved so a rescan does not read the moved file as
// changed content.
//
// Only regular files are handled: the executor's move steps are always files
// (directories are covered by the mkdir/rmdir steps), and silently deep-copying
// a directory tree here would hide a planner bug rather than surface it.
func moveAcrossDevices(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("cross-device move of non-regular file %s", src)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	// Closed explicitly below rather than deferred: Windows refuses to remove a
	// file that still has an open handle, so the source must be closed before
	// the os.Remove that completes the move.
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".abrcopy-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Any failure from here on must not leave the partial copy behind.
	cleanup := func(cause error) error {
		tmp.Close()
		os.Remove(tmpName)
		return cause
	}
	if _, err := io.Copy(tmp, in); err != nil {
		return cleanup(fmt.Errorf("copy %s -> %s: %w", src, dst, err))
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("flush %s: %w", dst, err))
	}
	if err := tmp.Close(); err != nil {
		return cleanup(fmt.Errorf("close %s: %w", dst, err))
	}
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		return cleanup(fmt.Errorf("chmod %s: %w", dst, err))
	}
	if err := os.Chtimes(tmpName, time.Now(), fi.ModTime()); err != nil {
		return cleanup(fmt.Errorf("set mtime on %s: %w", dst, err))
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return cleanup(fmt.Errorf("place %s: %w", dst, err))
	}
	if err := in.Close(); err != nil {
		return fmt.Errorf("close %s: %w", src, err)
	}
	// dst is complete and durable; dropping the source is what makes this a
	// move. If the source can't be removed (a read-only mergerfs branch, an
	// immutable file, a directory the process can't write) undo the copy we
	// just placed so the tree is left exactly as we found it — a stranded
	// duplicate at dst would also defeat the caller's rollback, which can't
	// rmdir a directory that now holds it. Only if that cleanup also fails is
	// the file genuinely in two places, and the error says so.
	if err := os.Remove(src); err != nil {
		if rmErr := os.Remove(dst); rmErr != nil {
			return fmt.Errorf("copied to %s but could not remove %s (%w); also could not remove the copy: %v", dst, src, err, rmErr)
		}
		return fmt.Errorf("could not remove source %s after copying it: %w", src, err)
	}
	return nil
}

// checkMoveContainment re-runs the containment guard for a single move
// immediately before its real os.Rename: both endpoints must still resolve
// inside root and src must not have become a symlink. This closes the TOCTOU
// window between the up-front batch check and the actual rename. Error wording
// matches the batch check so behaviour is identical either way.
func checkMoveContainment(root, src, dst string) error {
	if !pathguard.ResolvedWithinRoot(root, src) || !pathguard.ResolvedWithinRoot(root, dst) {
		return fmt.Errorf("refusing move outside library root: %s -> %s", src, dst)
	}
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to move symlink: %s", src)
	}
	return nil
}

// moveStep is one planned filesystem move (or case-fix) Execute is about to
// perform: kind is OpMove/OpCaseFix, src/dst are absolute paths. It is the
// near-twin of completedStep below but deliberately NOT merged: a moveStep is
// intent (built up-front from the plan, may never happen if an earlier step
// fails) whereas a completedStep is history (appended only after the physical
// change lands, and replayed in reverse on rollback). The two have different
// lifetimes and are populated at different points.
type moveStep struct{ kind, src, dst string }

// completedStep is a filesystem step that succeeded, kept so a later failure
// can replay it in reverse. opID is the step's journal row, which rollback
// flips to "reverted" once the physical change is undone.
type completedStep struct{ opID, kind, src, dst string }

// rollback reverses completed steps, newest first, and returns every error it
// hit so the caller can report a partial rollback rather than swallowing it.
//
// Each reversed step is also flipped to "reverted" in the journal. That write
// is what keeps disk and journal in agreement after a failed run: a step left
// marked "done" would describe a move that no longer exists, and a later undo
// would try to reverse it a second time. A step whose reversal failed keeps its
// "done" status on purpose — it is still physically applied, so a subsequent
// undo of the failed job is exactly the retry it needs.
func rollback(database *db.DB, completed []completedStep) []error {
	var errs []error
	for i := len(completed) - 1; i >= 0; i-- {
		c := completed[i]
		var err error
		switch c.kind {
		case string(OpMove), string(OpCaseFix):
			if err = reverseMove(c.src, c.dst); err != nil {
				err = fmt.Errorf("restore %s -> %s: %w", c.dst, c.src, err)
			}
		case string(OpMkdir):
			if err = os.Remove(c.dst); err != nil && os.IsNotExist(err) {
				err = nil
			} else if err != nil {
				err = fmt.Errorf("remove created dir %s: %w", c.dst, err)
			}
		case string(OpRmdir):
			if err = os.MkdirAll(c.dst, 0o755); err != nil {
				err = fmt.Errorf("recreate pruned dir %s: %w", c.dst, err)
			}
		case string(OpTagWrite):
			// completed holds only steps this same, still-running Execute just
			// performed, and the organize/undo gate (Service.sem) guarantees
			// nothing else can have touched this path meanwhile — so unlike
			// Undo's reversal of a possibly old, separate job, isOwner here
			// should always be true. A false one still isn't treated as a hard
			// rollback failure (the rest of the job needs to keep unwinding),
			// but it is logged loudly: it means a tag-write this run itself just
			// made is being left in place.
			isOwner, rerr := restoreTagFile(database, c.opID, c.src, c.dst)
			if rerr != nil {
				err = fmt.Errorf("restore tags for %s: %w", c.dst, rerr)
			} else if !isOwner {
				slog.Error("rollback could not restore tags it just wrote: backup ownership already lost",
					"op", c.opID, "file", c.dst)
			} else if rmErr := os.Remove(c.src); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Warn("could not remove consumed tag backup", "path", c.src, "err", rmErr)
			}
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := database.MarkRenameOp(c.opID, "reverted", ""); err != nil {
			errs = append(errs, fmt.Errorf("journal rollback of step %s: %w", c.opID, err))
		}
	}
	return errs
}

// rebase converts fileID->rootRelPath into fileID->pathRelativeToNewSourceDir,
// which is how book_files.rel_path is stored.
func rebase(rootRel map[string]string, newSourceDir, root string) map[string]string {
	out := make(map[string]string, len(rootRel))
	for id, rel := range rootRel {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		r, err := filepath.Rel(newSourceDir, abs)
		if err != nil {
			r = filepath.Base(abs)
		}
		out[id] = filepath.ToSlash(r)
	}
	return out
}

// uniqueSorted returns the distinct entries of in, ordered by path depth. It
// allocates its own slice rather than filtering into in[:0], which would
// overwrite the caller's backing array.
func uniqueSorted(in []string, deepFirst bool) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if deepFirst {
			return len(out[i]) > len(out[j])
		}
		return len(out[i]) < len(out[j])
	})
	return out
}
