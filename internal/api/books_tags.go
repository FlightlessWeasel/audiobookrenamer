package api

import (
	"errors"
	"fmt"
	"net/http"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/organize"
)

// maxTagStatusIDs bounds one request: each id costs at least one file read
// (more for a multi-file book), so an unbounded batch could turn one request
// into an arbitrarily large disk scan.
const maxTagStatusIDs = 200

type tagStatusRequest struct {
	IDs []string `json:"ids"`
}

// tagStatus is one book's tag-drift check: whether its files' currently
// embedded tags match what organize would write from its accepted metadata.
// Match is computed even when the library has write_tags off, so the UI can
// show what turning it on would do; Enabled is what tells a caller whether a
// "mismatch" here means something is wrong or is simply expected.
type tagStatus struct {
	ID      string                 `json:"id"`
	Title   string                 `json:"title,omitempty"`
	Enabled bool                   `json:"enabled"`
	Match   string                 `json:"match"` // "match" | "mismatch" | "unsupported" | "unmatched"
	Files   []organize.TagFilePlan `json:"files,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

type tagStatusResponse struct {
	Books []tagStatus `json:"books"`
}

// tagStatusBooks reports, for each requested book, whether its files' embedded
// tags currently match its accepted metadata. Each book is checked
// independently: one book's failure (not found, its library gone, a file that
// can't be read) is recorded on that book alone rather than failing the whole
// request, matching the batch-delete endpoint's convention.
func (s *Server) tagStatusBooks(w http.ResponseWriter, r *http.Request) {
	var req tagStatusRequest
	if !s.decode(w, r, &req) {
		return
	}
	ids := dedupeIDs(req.IDs)
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(ids) > maxTagStatusIDs {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("at most %d ids per request", maxTagStatusIDs))
		return
	}

	// Libraries are looked up once per id encountered, not once per book: many
	// requested books typically share the same library.
	libs := map[string]model.Library{}
	out := make([]tagStatus, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.checkOneBookTags(id, libs))
	}
	writeJSON(w, http.StatusOK, tagStatusResponse{Books: out})
}

func (s *Server) checkOneBookTags(id string, libs map[string]model.Library) tagStatus {
	st := tagStatus{ID: id}
	b, err := s.DB.GetBook(id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			st.Error = "book not found"
		} else {
			st.Error = err.Error()
		}
		return st
	}
	st.Title = b.Title

	// An unmatched (or needs-review, or error) book has no accepted metadata to
	// compare against; "does the file match the DB" is not yet a meaningful
	// question for it.
	if b.State != model.StateMatched && b.State != model.StateOrganized {
		st.Match = "unmatched"
		return st
	}

	lib, ok := libs[b.LibraryID]
	if !ok {
		lib, err = s.DB.GetLibrary(b.LibraryID)
		if err != nil {
			st.Error = "load library: " + err.Error()
			return st
		}
		libs[b.LibraryID] = lib
	}
	st.Enabled = lib.WriteTags
	st.Files = organize.CheckBookTags(s.DB, lib, b)
	st.Match = summarizeTagMatch(st.Files)
	return st
}

// summarizeTagMatch rolls up a book's per-file plan into one word: "mismatch"
// if any writable file would be rewritten, "match" if every writable file
// already carries the right tags, "unsupported" if the book has no writable
// file at all (nothing to compare).
func summarizeTagMatch(files []organize.TagFilePlan) string {
	anyWritable := false
	for _, f := range files {
		if !f.Writable {
			continue
		}
		anyWritable = true
		if f.Changed {
			return "mismatch"
		}
	}
	if !anyWritable {
		return "unsupported"
	}
	return "match"
}
