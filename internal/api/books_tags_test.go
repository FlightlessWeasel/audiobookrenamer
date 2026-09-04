package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/tagwrite"
)

// seedTaggedBook creates a matched, single-file book on disk (real enough
// bytes for an ID3v2/MP4 reader to parse as "no tag" rather than error) and
// returns it plus its absolute file path.
func seedTaggedBook(t *testing.T, s *Server, lib model.Library, ext string, writeTags bool) (model.Book, string) {
	t.Helper()
	if writeTags != lib.WriteTags {
		lib.WriteTags = writeTags
		var err error
		lib, err = s.DB.UpdateLibrary(lib)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(lib.RootPath, "book"+ext)
	if err := os.WriteFile(path, []byte("placeholder audio content, long enough to parse"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: lib.RootPath, SourceFile: path,
		Layout: model.LayoutSingle, State: model.StateUnmatched,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.ReplaceBookFiles(b.ID, []model.BookFile{{RelPath: "book" + ext, Ext: ext, Track: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.SetBookMatch(b.ID, model.Book{
		ID: b.ID, State: model.StateMatched, Title: "Elantris", Author: "Brandon Sanderson",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.DB.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out, path
}

func postTagStatus(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/books/tag-status", strings.NewReader(body)).
		WithContext(context.Background())
	rr := httptest.NewRecorder()
	s.tagStatusBooks(rr, req)
	return rr
}

func decodeTagStatus(t *testing.T, rr *httptest.ResponseRecorder) tagStatusResponse {
	t.Helper()
	var resp tagStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rr.Body.String())
	}
	return resp
}

func TestTagStatusBooks_Mismatch(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := seedTaggedBook(t, s, lib, ".mp3", true)

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q]}`, b.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	resp := decodeTagStatus(t, rr)
	if len(resp.Books) != 1 {
		t.Fatalf("books = %d, want 1", len(resp.Books))
	}
	got := resp.Books[0]
	if !got.Enabled || got.Match != "mismatch" || got.Error != "" {
		t.Fatalf("got %+v, want enabled+mismatch", got)
	}
	if len(got.Files) != 1 || !got.Files[0].Writable || !got.Files[0].Changed {
		t.Fatalf("files = %+v", got.Files)
	}
}

func TestTagStatusBooks_MatchAfterWriting(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, path := seedTaggedBook(t, s, lib, ".mp3", true)

	w, err := tagwrite.WriterFor(".mp3")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(path, tagwrite.Desired(b, b.Files[0], 1)); err != nil {
		t.Fatalf("write tags: %v", err)
	}

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q]}`, b.ID))
	resp := decodeTagStatus(t, rr)
	if resp.Books[0].Match != "match" {
		t.Fatalf("match = %q, want match: %+v", resp.Books[0].Match, resp.Books[0])
	}
}

func TestTagStatusBooks_UnsupportedFormat(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := seedTaggedBook(t, s, lib, ".ogg", true)

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q]}`, b.ID))
	resp := decodeTagStatus(t, rr)
	if resp.Books[0].Match != "unsupported" {
		t.Fatalf("match = %q, want unsupported", resp.Books[0].Match)
	}
}

// Match is computed even when write_tags is off, so the UI can preview what
// turning it on would do; Enabled says it isn't live yet.
func TestTagStatusBooks_ComputedEvenWhenWriteTagsOff(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := seedTaggedBook(t, s, lib, ".mp3", false)

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q]}`, b.ID))
	resp := decodeTagStatus(t, rr)
	got := resp.Books[0]
	if got.Enabled {
		t.Fatal("enabled = true, want false")
	}
	if got.Match != "mismatch" {
		t.Fatalf("match = %q, want mismatch (still computed)", got.Match)
	}
}

func TestTagStatusBooks_UnmatchedBookSkipsFileRead(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{
		Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst, WriteTags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// No file on disk at all: if the handler tried to read it, this would
	// surface as an error rather than the expected "unmatched" bucket.
	b, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: lib.RootPath, SourceFile: filepath.Join(lib.RootPath, "book.mp3"),
		Layout: model.LayoutSingle, State: model.StateUnmatched,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.ReplaceBookFiles(b.ID, []model.BookFile{{RelPath: "book.mp3", Ext: ".mp3", Track: 1}}); err != nil {
		t.Fatal(err)
	}

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q]}`, b.ID))
	resp := decodeTagStatus(t, rr)
	got := resp.Books[0]
	if got.Match != "unmatched" || got.Error != "" {
		t.Fatalf("got %+v, want unmatched with no error", got)
	}
}

// One book's failure doesn't fail the whole batch.
func TestTagStatusBooks_UnknownIDReportedPerBook(t *testing.T) {
	s := newTestServer(t)
	lib, err := s.DB.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := seedTaggedBook(t, s, lib, ".mp3", true)

	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%q,"does-not-exist"]}`, b.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	resp := decodeTagStatus(t, rr)
	if len(resp.Books) != 2 {
		t.Fatalf("books = %d, want 2", len(resp.Books))
	}
	if resp.Books[0].Match != "mismatch" {
		t.Fatalf("known book: %+v", resp.Books[0])
	}
	if resp.Books[1].Error == "" {
		t.Fatalf("unknown book: expected an error, got %+v", resp.Books[1])
	}
}

func TestTagStatusBooks_RequiresIDs(t *testing.T) {
	s := newTestServer(t)
	rr := postTagStatus(t, s, `{"ids":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestTagStatusBooks_CapsIDCount(t *testing.T) {
	s := newTestServer(t)
	ids := make([]string, maxTagStatusIDs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("%q", fmt.Sprintf("id%d", i))
	}
	rr := postTagStatus(t, s, fmt.Sprintf(`{"ids":[%s]}`, strings.Join(ids, ",")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
