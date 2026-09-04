package api

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/pathguard"

	"github.com/go-chi/chi/v5"
)

// deleteBook removes one book's audio from disk and then its database rows.
// Disk first: if the database delete then fails the leftover row points at
// files that no longer exist and the next scan prunes it, whereas deleting the
// row first and failing on disk would leave orphaned files a scan would re-add.
func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	b, err := s.DB.GetBookBare(id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if err := s.removeBookFromDisk(b); err != nil {
		if errors.Is(err, pathguard.ErrRefused) {
			// The stored path can't be deleted safely (it is or escapes the
			// library root, or is a symlink). That is a property of the data,
			// not a transient failure, so it is a client error, not a 500.
			writeErr(w, http.StatusUnprocessableEntity, "cannot delete this book's files: "+err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "delete files: "+err.Error())
		return
	}
	if err := s.DB.DeleteBooks([]string{id}); err != nil {
		writeDBErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type deleteBooksRequest struct {
	IDs []string `json:"ids"`
}

type deleteFailure struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Error string `json:"error"`
}

type deleteBooksResult struct {
	Deleted int             `json:"deleted"`
	Failed  []deleteFailure `json:"failed,omitempty"`
}

// deleteBooks removes several books at once. Each book's disk removal is
// attempted independently; only the ones that succeed have their rows deleted,
// and the response reports which failed and why so a partial run is legible.
func (s *Server) deleteBooks(w http.ResponseWriter, r *http.Request) {
	var req deleteBooksRequest
	if !s.decode(w, r, &req) {
		return
	}

	ids := dedupeIDs(req.IDs)
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}

	var toDelete []string
	var failed []deleteFailure
	for _, id := range ids {
		b, err := s.DB.GetBookBare(id)
		if errors.Is(err, db.ErrNotFound) {
			continue // already gone — nothing to do, not a failure
		}
		if err != nil {
			failed = append(failed, deleteFailure{ID: id, Error: err.Error()})
			continue
		}
		if err := s.removeBookFromDisk(b); err != nil {
			failed = append(failed, deleteFailure{ID: id, Title: b.Title, Error: err.Error()})
			continue
		}
		toDelete = append(toDelete, id)
	}

	deleted := len(toDelete)
	if deleted > 0 {
		if err := s.DB.DeleteBooks(toDelete); err != nil {
			// The files are already gone but the rows survived. Surface that as
			// per-book failures rather than a bare 500 that hides which books
			// are now half-deleted; the next scan prunes the stale rows.
			deleted = 0
			for _, id := range toDelete {
				failed = append(failed, deleteFailure{ID: id, Error: "files deleted but row not removed: " + err.Error()})
			}
		}
	}
	writeJSON(w, http.StatusOK, deleteBooksResult{Deleted: deleted, Failed: failed})
}

// removeBookFromDisk deletes the on-disk audio for b, keeping every deletion
// strictly inside the owning library's root (pathguard enforces this and
// refuses symlinks and the root itself).
func (s *Server) removeBookFromDisk(b model.Book) error {
	lib, err := s.DB.GetLibrary(b.LibraryID)
	if err != nil {
		return fmt.Errorf("load library: %w", err)
	}
	root := lib.RootPath

	// A legacy loose-file book is one file that may share its folder with other
	// books: remove just that file, then tidy the folder if it empties.
	if b.SourceFile != "" {
		if err := pathguard.RemoveWithin(root, b.SourceFile, false); err != nil {
			return err
		}
		pathguard.PruneEmptyParents(root, filepath.Dir(b.SourceFile))
		return nil
	}

	// A book whose audio sits directly in the library root has no folder of its
	// own — removing SourceDir would be removing the root. Delete its tracked
	// files one by one instead.
	if pathguard.SamePath(root, b.SourceDir) {
		full, err := s.DB.GetBook(b.ID)
		if err != nil {
			return err
		}
		for _, f := range full.Files {
			abs := filepath.Join(b.SourceDir, filepath.FromSlash(f.RelPath))
			if err := pathguard.RemoveWithin(root, abs, false); err != nil {
				return err
			}
		}
		return nil
	}

	// The common case: the folder holds exactly this one book. Remove it whole,
	// including any CD1/CD2 disc subfolders the scanner merged into the unit,
	// then tidy any now-empty author/series folders above it.
	if err := pathguard.RemoveWithin(root, b.SourceDir, true); err != nil {
		return err
	}
	pathguard.PruneEmptyParents(root, filepath.Dir(b.SourceDir))
	return nil
}

// dedupeIDs drops blanks and repeats from a batch request's id list, keeping
// first-seen order, so a caller's accidental duplicate can't be double-counted
// (a double delete) or double-charged against a request's id cap (tag-status).
func dedupeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
