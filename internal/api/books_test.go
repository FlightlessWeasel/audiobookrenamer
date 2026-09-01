package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

func seedBook(t *testing.T, s *Server) model.Book {
	t.Helper()
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: t.TempDir(), Layout: model.LayoutSingle,
		State: model.StateMatched, Title: "Dune", Author: "Frank Herbert",
		AuthorSort: "Herbert, Frank", AuthorSortSource: model.AuthorSortDerived,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func patchBook(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+id, strings.NewReader(body)).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.patchBook(rr, req)
	return rr
}

// PATCH /api/books/{id} with author_sort persists the value and flips the
// provenance to "manual" so a later match won't recompute over it.
func TestPatchBook_SetsAuthorSortManual(t *testing.T) {
	s := newTestServer(t)
	b := seedBook(t, s)

	rr := patchBook(t, s, b.ID, `{"author_sort":"Le Guin, Ursula K."}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got model.Book
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.AuthorSort != "Le Guin, Ursula K." {
		t.Fatalf("response AuthorSort = %q", got.AuthorSort)
	}
	if got.AuthorSortSource != model.AuthorSortManual {
		t.Fatalf("response AuthorSortSource = %q, want manual", got.AuthorSortSource)
	}

	reloaded, err := s.DB.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthorSort != "Le Guin, Ursula K." || reloaded.AuthorSortSource != model.AuthorSortManual {
		t.Fatalf("persisted AuthorSort=%q source=%q", reloaded.AuthorSort, reloaded.AuthorSortSource)
	}
}

func TestPatchBook_UnknownIs404(t *testing.T) {
	s := newTestServer(t)
	rr := patchBook(t, s, "does-not-exist", `{"author_sort":"X"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestPatchBook_NoFieldsIs400(t *testing.T) {
	s := newTestServer(t)
	b := seedBook(t, s)
	rr := patchBook(t, s, b.ID, `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
