package api

import (
	"net/http"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

type booksResponse struct {
	Books  []model.Book   `json:"books"`
	Counts map[string]int `json:"counts"` // state -> count, for the whole (optionally library-scoped) set
}

func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := parseLimit(w, r, 2000, 5000)
	if !ok {
		return
	}
	filter := db.BookFilter{
		LibraryID: q.Get("library_id"),
		State:     model.BookState(q.Get("state")),
		Query:     q.Get("q"),
		Limit:     limit,
	}
	books, err := s.DB.ListBooks(filter)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if books == nil {
		books = []model.Book{}
	}
	counts, err := s.DB.CountBooksByState(filter.LibraryID)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, booksResponse{Books: books, Counts: counts})
}

func (s *Server) getBook(w http.ResponseWriter, r *http.Request) {
	book, err := s.DB.GetBook(chi.URLParam(r, "id"))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, book)
}

// bookPatch is the set of hand-editable book fields. A nil pointer means "leave
// this field alone"; a present pointer (including "") is applied.
type bookPatch struct {
	AuthorSort *string `json:"author_sort"`
}

func (s *Server) patchBook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.DB.GetBookBare(id); err != nil {
		writeDBErr(w, err)
		return
	}
	var in bookPatch
	if !s.decode(w, r, &in) {
		return
	}
	if in.AuthorSort == nil {
		writeErr(w, http.StatusBadRequest, "no editable fields in request")
		return
	}

	// An explicit author_sort edit is a manual override: record it as such so a
	// later metadata match won't recompute over it.
	authorSort := strings.TrimSpace(*in.AuthorSort)
	if err := s.DB.SetBookAuthorSort(id, authorSort, model.AuthorSortManual); err != nil {
		writeDBErr(w, err)
		return
	}
	book, err := s.DB.GetBook(id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, book)
}
