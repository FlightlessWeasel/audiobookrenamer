package api

import (
	"net/http"
	"strconv"

	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

// parseLimit reads a ?limit= query param, returning def when it is absent and
// writing a 400 (and returning ok=false) when it is present but not an integer
// in [1, max].
func parseLimit(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		writeErr(w, http.StatusBadRequest, "limit must be an integer between 1 and "+strconv.Itoa(max))
		return 0, false
	}
	return n, true
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit, ok := parseLimit(w, r, 100, 500)
	if !ok {
		return
	}
	jobs, err := s.DB.ListJobs(limit)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if jobs == nil {
		jobs = []model.Job{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.DB.GetJob(chi.URLParam(r, "id"))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.DB.GetJob(id); err != nil {
		writeDBErr(w, err)
		return
	}
	s.Worker.Cancel(id)
	w.WriteHeader(http.StatusAccepted)
}
