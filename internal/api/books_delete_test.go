package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

// seedBookOnDisk creates a library rooted at a temp dir and a folder-layout
// book with a real audio file under it, so the delete handlers have something
// to remove from disk.
func seedBookOnDisk(t *testing.T, s *Server) (model.Library, model.Book, string) {
	t.Helper()
	root := t.TempDir()
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	bookDir := filepath.Join(root, "Frank Herbert", "Dune")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "dune.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.UpsertBookWithFiles(
		model.Book{
			LibraryID: lib.ID, SourceDir: bookDir, Layout: model.LayoutSingle,
			State: model.StateMatched, Title: "Dune",
		},
		[]model.BookFile{{RelPath: "dune.m4b", Size: 5, Ext: ".m4b"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return lib, b, bookDir
}

func TestDeleteBook_RemovesFolderAndRow(t *testing.T) {
	s := newTestServer(t)
	_, b, bookDir := seedBookOnDisk(t, s)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", b.ID)
	req := httptest.NewRequest(http.MethodDelete, "/api/books/"+b.ID, nil).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.deleteBook(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(bookDir); !os.IsNotExist(err) {
		t.Fatalf("book folder still on disk: %v", err)
	}
	if _, err := s.DB.GetBookBare(b.ID); err == nil {
		t.Fatal("book row still in db")
	}
}

func TestDeleteBooks_BulkReportsResult(t *testing.T) {
	s := newTestServer(t)
	_, b1, dir1 := seedBookOnDisk(t, s)
	_, b2, dir2 := seedBookOnDisk(t, s)

	body := `{"ids":["` + b1.ID + `","` + b2.ID + `","` + b1.ID + `","missing"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/books/delete", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.deleteBooks(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got deleteBooksResult
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (%+v)", got.Deleted, got)
	}
	if len(got.Failed) != 0 {
		t.Fatalf("failed = %+v, want none", got.Failed)
	}
	for _, d := range []string{dir1, dir2} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("%s still on disk: %v", d, err)
		}
	}
	for _, id := range []string{b1.ID, b2.ID} {
		if _, err := s.DB.GetBookBare(id); err == nil {
			t.Fatalf("book %s still in db", id)
		}
	}
}

// A legacy loose single file shares its folder with siblings: only that file
// is removed, and the folder is left because it still holds another book.
func TestDeleteBook_LooseFileLeavesSharedFolder(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "Loose")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(shared, "mine.m4b")
	sibling := filepath.Join(shared, "sibling.m4b")
	for _, p := range []string{mine, sibling} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: shared, SourceFile: mine,
		Layout: model.LayoutSingle, State: model.StateMatched,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.removeBookFromDisk(b); err != nil {
		t.Fatalf("removeBookFromDisk: %v", err)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("the book's own file should be gone: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("the sibling's file must be kept: %v", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatalf("the shared folder must be kept: %v", err)
	}
}

// A book whose audio sits directly in the library root has its tracked files
// removed one by one; the root itself is never touched.
func TestDeleteBook_RootResidentKeepsRoot(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(root, "book.m4b")
	if err := os.WriteFile(audio, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.UpsertBookWithFiles(
		model.Book{LibraryID: lib.ID, SourceDir: root, Layout: model.LayoutSingle, State: model.StateMatched},
		[]model.BookFile{{RelPath: "book.m4b", Size: 1, Ext: ".m4b"}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.removeBookFromDisk(b); err != nil {
		t.Fatalf("removeBookFromDisk: %v", err)
	}
	if _, err := os.Stat(audio); !os.IsNotExist(err) {
		t.Fatalf("the audio file should be gone: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the library root must survive: %v", err)
	}
}

func TestDeleteBooks_EmptyIsBadRequest(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/books/delete", strings.NewReader(`{"ids":[]}`))
	rr := httptest.NewRecorder()
	s.deleteBooks(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// A book row whose SourceDir escapes the library root must not be deleted from
// disk, and its row must be kept so the state stays consistent.
func TestDeleteBook_RefusesOutsideRoot(t *testing.T) {
	s := newTestServer(t)
	lib, _, _ := seedBookOnDisk(t, s)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rogue, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: outside, Layout: model.LayoutSingle, State: model.StateMatched,
	})
	if err != nil {
		t.Fatal(err)
	}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", rogue.ID)
	req := httptest.NewRequest(http.MethodDelete, "/api/books/"+rogue.ID, nil).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.deleteBook(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body: %s)", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("file outside root should be untouched: %v", err)
	}
	if _, err := s.DB.GetBookBare(rogue.ID); err != nil {
		t.Fatalf("row should be kept when disk delete refused: %v", err)
	}
}
