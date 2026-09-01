package api

import (
	"errors"
	"log/slog"
	"net/http"

	"audiobookrenamer/internal/metadata"
	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

func (s *Server) listCandidates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.DB.GetBookBare(id); err != nil {
		writeDBErr(w, err)
		return
	}
	cands, err := s.DB.ListCandidates(id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if cands == nil {
		cands = []model.Candidate{}
	}
	writeJSON(w, http.StatusOK, cands)
}

type matchRequest struct {
	Auto       bool   `json:"auto"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Manual     *struct {
		Title       string `json:"title"`
		Subtitle    string `json:"subtitle"`
		Author      string `json:"author"`
		Narrator    string `json:"narrator"`
		Series      string `json:"series"`
		SeriesIndex string `json:"series_index"`
		Year        int    `json:"year"`
		ASIN        string `json:"asin"`
		ISBN        string `json:"isbn"`
		CoverURL    string `json:"cover_url"`
	} `json:"manual"`
}

type matchResponse struct {
	Book       model.Book        `json:"book"`
	Candidates []model.Candidate `json:"candidates,omitempty"`
}

func (s *Server) matchBook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req matchRequest
	if !s.decode(w, r, &req) {
		return
	}

	switch {
	case req.Manual != nil:
		m := req.Manual
		c := model.Candidate{
			Provider: "manual", ProviderID: "manual",
			Title: m.Title, Subtitle: m.Subtitle, Series: m.Series, SeriesIndex: m.SeriesIndex,
			Year: m.Year, ASIN: m.ASIN, ISBN: m.ISBN, CoverURL: m.CoverURL, Score: 1,
		}
		if m.Author != "" {
			c.Authors = []string{m.Author}
		}
		if m.Narrator != "" {
			c.Narrators = []string{m.Narrator}
		}
		book, err := s.Matcher.AcceptCandidate(id, c)
		if err != nil {
			writeDBErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, matchResponse{Book: book})

	case req.Provider != "" && req.ProviderID != "":
		book, err := s.Matcher.AcceptStored(id, req.Provider, req.ProviderID)
		if err != nil {
			writeDBErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, matchResponse{Book: book})

	default: // auto
		book, cands, err := s.Matcher.MatchBook(r.Context(), id)
		if err != nil {
			slog.Warn("auto-match failed", "book", id, "err", err)
			writeErr(w, http.StatusBadGateway, "match failed: no metadata provider returned a usable result")
			return
		}
		writeJSON(w, http.StatusOK, matchResponse{Book: book, Candidates: cands})
	}
}

type searchRequest struct {
	Q        string `json:"q"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Year     int    `json:"year"`
	Provider string `json:"provider"` // "" = every enabled provider
}

func (s *Server) searchMetadata(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !s.decode(w, r, &req) {
		return
	}
	q := metadata.Query{Freeform: req.Q, Title: req.Title, Author: req.Author, Year: req.Year}
	cands, err := s.Matcher.Search(r.Context(), q, req.Provider)
	if err != nil {
		if errors.Is(err, metadata.ErrProviderNotAvailable) {
			writeErr(w, http.StatusBadRequest, "unknown or disabled metadata provider")
			return
		}
		slog.Warn("metadata search failed", "err", err)
		writeErr(w, http.StatusBadGateway, "search failed: no metadata provider is reachable right now")
		return
	}
	if cands == nil {
		cands = []model.Candidate{}
	}
	writeJSON(w, http.StatusOK, cands)
}

// acceptTopRequest asks to bulk-accept stored top candidates. LibraryID is
// optional; empty means every library.
type acceptTopRequest struct {
	LibraryID string  `json:"library_id"`
	MinScore  float64 `json:"min_score"`
}

func (s *Server) acceptTopCandidates(w http.ResponseWriter, r *http.Request) {
	var req acceptTopRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.MinScore < 0 || req.MinScore > 1 {
		writeErr(w, http.StatusBadRequest, "min_score must be between 0 and 1")
		return
	}
	if req.LibraryID != "" {
		if _, err := s.DB.GetLibrary(req.LibraryID); err != nil {
			writeDBErr(w, err)
			return
		}
	}
	out, err := s.Matcher.AcceptTopCandidates(req.LibraryID, req.MinScore)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) matchLibrary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.DB.GetLibrary(id); err != nil {
		writeDBErr(w, err)
		return
	}
	job, err := s.Worker.Enqueue(model.JobMatch, id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
