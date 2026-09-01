package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/organize"

	"github.com/go-chi/chi/v5"
)

// checkOrganizeRequest validates the library and that every requested book
// belongs to it, writing the appropriate error response and returning false on
// failure.
func (s *Server) checkOrganizeRequest(w http.ResponseWriter, req organizeRequest) bool {
	if req.LibraryID == "" {
		writeErr(w, http.StatusBadRequest, "library_id is required")
		return false
	}
	if _, err := s.DB.GetLibrary(req.LibraryID); err != nil {
		writeDBErr(w, err)
		return false
	}
	if err := organize.ValidateBooks(s.DB, req.LibraryID, req.BookIDs); err != nil {
		if errors.Is(err, organize.ErrBookNotInLibrary) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return false
		}
		writeDBErr(w, err)
		return false
	}
	return true
}

type organizeRequest struct {
	LibraryID string   `json:"library_id"`
	BookIDs   []string `json:"book_ids"`
}

func (s *Server) organizePreview(w http.ResponseWriter, r *http.Request) {
	var req organizeRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !s.checkOrganizeRequest(w, req) {
		return
	}
	plan, err := organize.BuildPlan(s.DB, req.LibraryID, req.BookIDs)
	if err != nil {
		if errors.Is(err, organize.ErrBookNotInLibrary) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) organizeApply(w http.ResponseWriter, r *http.Request) {
	var req organizeRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !s.checkOrganizeRequest(w, req) {
		return
	}
	payload, _ := json.Marshal(organize.OrganizePayload{BookIDs: req.BookIDs})
	job, err := s.Worker.EnqueuePayload(model.JobOrganize, req.LibraryID, string(payload))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) undoJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := s.DB.GetJob(id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if target.Type != model.JobOrganize {
		writeErr(w, http.StatusBadRequest, "only organize jobs can be undone")
		return
	}
	// Only a job that has stopped can be undone. A failed or canceled organize
	// run is explicitly undoable: its executor rolls back what it had already
	// applied and marks those journal steps "reverted", but a rollback can
	// itself fail partway (a permission error, a disk that filled) and leave
	// steps still marked "done" — that is, files still sitting at their new
	// paths. Undo is the retry for exactly those steps, and refusing it here
	// would leave a half-renamed library with no supported way back.
	if target.Status == model.JobQueued || target.Status == model.JobRunning {
		writeErr(w, http.StatusConflict, "job is still running; wait for it to finish before undoing")
		return
	}
	// Refuse a second undo for the same organize job while one is already
	// queued or running: two concurrent undo runs would interleave their
	// filesystem/journal reversals. (The organize service also serializes
	// execution, and Undo is idempotent, so this is defence in depth against
	// the common double-click.)
	if active, err := s.DB.ActiveUndoExists(id); err != nil {
		writeDBErr(w, err)
		return
	} else if active {
		writeErr(w, http.StatusConflict, "an undo for this job is already pending or running")
		return
	}
	payload, _ := json.Marshal(organize.UndoPayload{TargetJobID: id})
	job, err := s.Worker.EnqueuePayload(model.JobUndo, target.LibraryID, string(payload))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
