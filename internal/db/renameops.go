package db

import (
	"database/sql"
	"errors"
	"fmt"

	"audiobookrenamer/internal/model"

	"github.com/google/uuid"
)

// RenameOp is one journaled step of an organize job.
type RenameOp struct {
	ID     string
	JobID  string
	Seq    int
	Kind   string
	Src    string
	Dst    string
	Status string // pending | done | failed | reverted
	Error  string
}

// InsertRenameOp records a step as pending and returns its id.
func (d *DB) InsertRenameOp(jobID string, seq int, kind, src, dst string) (string, error) {
	id := uuid.NewString()
	_, err := d.Exec(
		`INSERT INTO rename_ops (id, job_id, seq, op, src, dst, status, error)
		 VALUES (?,?,?,?,?,?, 'pending', '')`,
		id, jobID, seq, kind, src, dst,
	)
	return id, err
}

// MarkRenameOp updates a step's status and error text.
func (d *DB) MarkRenameOp(id, status, errMsg string) error {
	_, err := d.Exec(`UPDATE rename_ops SET status=?, error=? WHERE id=?`, status, errMsg, id)
	return err
}

// CurrentTagBackupOwner returns the id and backup path (its src column) of the
// most recent successful ("done") tagwrite op for dst, across every job — not
// just the one currently running. Only one tag-write backup is ever kept per
// path: a later tag-write reuses and overwrites the same backup file rather
// than creating a new one, so an older tagwrite op's own backup reference can
// be stale. Comparing an op's id against the id this returns is how the
// executor and Undo tell whether that op is still the one the backup file on
// disk actually holds; ok is false when no tagwrite op has ever completed for
// dst (or all have since been reverted).
func (d *DB) CurrentTagBackupOwner(dst string) (opID, backupPath string, ok bool, err error) {
	err = d.QueryRow(
		`SELECT id, src FROM rename_ops WHERE op = ? AND dst = ? AND status = 'done'
		 ORDER BY rowid DESC LIMIT 1`,
		"tagwrite", dst,
	).Scan(&opID, &backupPath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return opID, backupPath, true, nil
}

// ListRenameOps returns a job's steps ordered by sequence.
func (d *DB) ListRenameOps(jobID string) ([]RenameOp, error) {
	rows, err := d.Query(
		`SELECT id, job_id, seq, op, src, dst, status, error FROM rename_ops WHERE job_id = ? ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenameOp
	for rows.Next() {
		var o RenameOp
		if err := rows.Scan(&o.ID, &o.JobID, &o.Seq, &o.Kind, &o.Src, &o.Dst, &o.Status, &o.Error); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateBookLocation moves a book's stored location and sets its state. Pass
// sourceFile "" for a book that now lives in its own folder.
func (d *DB) UpdateBookLocation(bookID, sourceDir, sourceFile string, state model.BookState) error {
	res, err := d.Exec(
		`UPDATE books SET source_dir=?, source_file=?, state=?, updated_at=? WHERE id=?`,
		sourceDir, sourceFile, state, now(), bookID,
	)
	if err != nil {
		return err
	}
	n, err := affected(res)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RestoreBookSnapshot atomically puts a book's location, state, and file paths
// back to a pre-organize snapshot (used by undo).
func (d *DB) RestoreBookSnapshot(bookID, sourceDir, sourceFile string, state model.BookState, fileRelByID map[string]string) error {
	return d.InTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE books SET source_dir=?, source_file=?, state=?, updated_at=? WHERE id=?`,
			sourceDir, sourceFile, state, now(), bookID,
		)
		if err != nil {
			return err
		}
		n, err := affected(res)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return setBookFileRelPaths(tx, bookID, fileRelByID)
	})
}

// setBookFileRelPaths updates rel_path for the given file ids, scoped to
// bookID with `AND book_id=?`. That scope still prevents cross-book
// corruption, but an id that matches no row for this book is now a hard error
// (rather than a silent no-op): callers run this inside a transaction, so the
// error rolls the whole finalize/restore back rather than letting the database
// describe a layout that isn't on disk.
func setBookFileRelPaths(x dbtx, bookID string, relByID map[string]string) error {
	for id, rel := range relByID {
		res, err := x.Exec(
			`UPDATE book_files SET rel_path=? WHERE id=? AND book_id=?`,
			rel, id, bookID,
		)
		if err != nil {
			return err
		}
		n, err := affected(res)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("book_files rel_path update matched no row: file=%s book=%s", id, bookID)
		}
	}
	return nil
}

// OrganizeFinal is the per-book result of a completed organize run, applied
// atomically by FinalizeOrganize.
type OrganizeFinal struct {
	BookID       string
	NewSourceDir string
	Snapshot     string            // JSON snapshot blob stored as the "snapshot" rename op
	FileRelByID  map[string]string // fileID -> new rel_path (relative to NewSourceDir)
}

// FinalizeOrganize writes, in a single transaction, the post-move snapshot rows
// and the updated book location / file paths for every book in a completed
// organize run. Either every row lands or none do, so the database can never be
// left describing a half-applied move. Snapshot ops are numbered from startSeq.
func (d *DB) FinalizeOrganize(jobID string, startSeq int, finals []OrganizeFinal) error {
	return d.InTx(func(tx *sql.Tx) error {
		seq := startSeq
		for _, f := range finals {
			if _, err := tx.Exec(
				`INSERT INTO rename_ops (id, job_id, seq, op, src, dst, status, error)
				 VALUES (?,?,?,?,?,?, 'done', '')`,
				uuid.NewString(), jobID, seq, "snapshot", f.BookID, f.Snapshot,
			); err != nil {
				return err
			}
			seq++
			res, err := tx.Exec(
				`UPDATE books SET source_dir=?, source_file='', state=?, updated_at=? WHERE id=?`,
				f.NewSourceDir, model.StateOrganized, now(), f.BookID,
			)
			if err != nil {
				return err
			}
			n, err := affected(res)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			if err := setBookFileRelPaths(tx, f.BookID, f.FileRelByID); err != nil {
				return err
			}
		}
		return nil
	})
}
