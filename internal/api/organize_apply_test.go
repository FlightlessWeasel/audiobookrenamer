package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/organize"
	"audiobookrenamer/internal/tagwrite"
	"audiobookrenamer/internal/worker"
)

// newTestServerWithWorker is newTestServer plus a real worker.Manager with the
// organize job type registered, so a test can drive /organize/apply through
// its actual HTTP-handler -> worker -> organize.Service path instead of
// calling organize.BuildPlan/Execute directly. Nothing else in this package
// exercises that path, which is exactly the seam a "the UI says success but
// nothing happened" class of bug hides in.
func newTestServerWithWorker(t *testing.T, backupDir string) *Server {
	t.Helper()
	s := newTestServer(t)
	wm := worker.New(s.DB, 1)
	t.Cleanup(wm.Shutdown)
	organize.Register(wm, organize.NewService(s.DB, backupDir))
	s.Worker = wm
	return s
}

// seedMatchedMP3Book creates a matched, single-file .mp3 book directly on
// disk and in the DB (bypassing a real scan/match, which this package doesn't
// wire up), with enough placeholder bytes for ID3v2 to parse as "no tag" per
// tagwrite's own tests, rather than as a truncated file.
func seedMatchedMP3Book(t *testing.T, s *Server, lib model.Library) model.Book {
	t.Helper()
	srcFile := filepath.Join(lib.RootPath, "incoming", "book.mp3")
	if err := os.MkdirAll(filepath.Dir(srcFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcFile, []byte("placeholder audio content, long enough to parse"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: filepath.Dir(srcFile), SourceFile: srcFile,
		Layout: model.LayoutSingle, State: model.StateUnmatched,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.ReplaceBookFiles(b.ID, []model.BookFile{{RelPath: "book.mp3", Ext: ".mp3", Track: 1}}); err != nil {
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
	return out
}

// applyAndWait POSTs /organize/apply for bookID exactly as the web UI does,
// then polls the job to completion (mirroring the frontend's waitForJob) and
// returns the book's post-apply state. It fails the test if the job doesn't
// finish "done" within the deadline.
func applyAndWait(t *testing.T, s *Server, libraryID, bookID string) model.Book {
	t.Helper()
	body := fmt.Sprintf(`{"library_id":%q,"book_ids":[%q]}`, libraryID, bookID)
	req := httptest.NewRequest(http.MethodPost, "/api/organize/apply", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.organizeApply(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("organize/apply status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var job model.Job
	if err := json.Unmarshal(rr.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var final model.Job
	for {
		var err error
		final, err = s.DB.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Status == model.JobDone || final.Status == model.JobFailed || final.Status == model.JobCanceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish in time, last status=%s", job.ID, final.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Status != model.JobDone {
		t.Fatalf("job %s status = %s, error = %s", job.ID, final.Status, final.Error)
	}

	updated, err := s.DB.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func readBackTags(t *testing.T, path string) tagwrite.TagSet {
	t.Helper()
	w, err := tagwrite.WriterFor(filepath.Ext(path))
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.Read(path)
	if err != nil {
		t.Fatalf("read tags at %s: %v", path, err)
	}
	return got
}

// Reproduces the UI's "Retag now" button end to end: HTTP -> worker ->
// organize.Service -> Execute -> tagwrite, for a book being organized for the
// first time.
func TestOrganizeApply_WritesTagsEndToEnd(t *testing.T) {
	s := newTestServerWithWorker(t, t.TempDir())
	lib, err := s.DB.CreateLibrary(model.Library{
		Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst, WriteTags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := seedMatchedMP3Book(t, s, lib)

	updated := applyAndWait(t, s, lib.ID, b.ID)
	target := filepath.Join(updated.SourceDir, updated.Files[0].RelPath)

	got := readBackTags(t, target)
	if got.Title != "Elantris" || got.Artist != "Brandon Sanderson" {
		t.Fatalf("tags not written: got title=%q artist=%q", got.Title, got.Artist)
	}
}

// The actual "Retag now" scenario: a book already organized before write_tags
// was turned on (so its file was moved but never tagged), then retagged in
// place once the library setting changes — the same sequence a user follows
// after enabling tag writing on an existing library.
func TestOrganizeApply_RetagAlreadyOrganizedBook(t *testing.T) {
	s := newTestServerWithWorker(t, t.TempDir())
	lib, err := s.DB.CreateLibrary(model.Library{
		Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst, WriteTags: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := seedMatchedMP3Book(t, s, lib)

	afterFirst := applyAndWait(t, s, lib.ID, b.ID)
	target := filepath.Join(afterFirst.SourceDir, afterFirst.Files[0].RelPath)

	lib.WriteTags = true
	if _, err := s.DB.UpdateLibrary(lib); err != nil {
		t.Fatal(err)
	}

	applyAndWait(t, s, lib.ID, b.ID)

	got := readBackTags(t, target)
	if got.Title != "Elantris" || got.Artist != "Brandon Sanderson" {
		t.Fatalf("retagging an already-organized book did not write tags: got title=%q artist=%q", got.Title, got.Artist)
	}
}
