package matcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

var tinyPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("cover bytes")...)

// coverServer serves tinyPNG and counts how many requests it received.
func coverServer(t *testing.T) (url string, hits *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func requireCover(t *testing.T, d *db.DB, bookID string) db.BookCover {
	t.Helper()
	c, ok, err := d.GetBookCover(bookID)
	if err != nil {
		t.Fatalf("GetBookCover: %v", err)
	}
	if !ok {
		t.Fatalf("book %s has no cached cover", bookID)
	}
	return c
}

func TestMatchBook_AutoAcceptFetchesCover(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	url, hits := coverServer(t)

	m := newMatcher(d, model.Candidate{
		Provider: "stub", ProviderID: "1",
		Title: "Dune", Authors: []string{"Frank Herbert"}, CoverURL: url,
	})
	if _, _, err := m.MatchBook(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}

	cov := requireCover(t, d, b.ID)
	if string(cov.Data) != string(tinyPNG) || cov.MIME != "image/png" || cov.SourceURL != url {
		t.Fatalf("cover = %+v", cov)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("cover server hit %d times, want 1", got)
	}
}

func TestAcceptStored_FetchesCover(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	url, _ := coverServer(t)
	if err := d.ReplaceCandidates(b.ID, []model.Candidate{
		{Provider: "p", ProviderID: "1", Title: "Dune", CoverURL: url},
	}); err != nil {
		t.Fatal(err)
	}

	m := newMatcher(d)
	if _, err := m.AcceptStored(context.Background(), b.ID, "p", "1"); err != nil {
		t.Fatal(err)
	}
	requireCover(t, d, b.ID)
}

func TestAcceptCandidate_FetchesCover(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	url, _ := coverServer(t)

	m := newMatcher(d)
	if _, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "manual", Title: "Dune", CoverURL: url,
	}); err != nil {
		t.Fatal(err)
	}
	requireCover(t, d, b.ID)
}

// A cover fetch that fails (a dead server) must not fail the match — a cover
// is an enhancement, never a requirement.
func TestFetchCover_FailureDoesNotFailTheMatch(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	srv.Close() // guarantees the fetch fails: nothing is listening

	m := newMatcher(d)
	book, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "manual", Title: "Dune", CoverURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("AcceptCandidate returned an error from a cover-fetch failure: %v", err)
	}
	if book.State != model.StateMatched {
		t.Fatalf("state = %q, want matched despite the cover fetch failing", book.State)
	}
	if _, ok, _ := d.GetBookCover(b.ID); ok {
		t.Fatal("a cover was stored despite the fetch failing")
	}
}

// A second match to a candidate with the same cover_url must not re-fetch.
func TestFetchCover_SkipsWhenAlreadyCached(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	url, hits := coverServer(t)
	m := newMatcher(d)

	c := model.Candidate{Provider: "manual", ProviderID: "manual", Title: "Dune", CoverURL: url}
	if _, err := m.AcceptCandidate(context.Background(), b.ID, c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptCandidate(context.Background(), b.ID, c); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("cover server hit %d times across two identical accepts, want 1", got)
	}
}

// Re-matching to a candidate with no cover at all must drop the previous one:
// a later organize must never embed an image that belonged to a different
// edition than the one now accepted.
func TestApply_InvalidatesCoverWhenNewCandidateHasNone(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	url, _ := coverServer(t)
	m := newMatcher(d)

	if _, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "1", Title: "Dune", CoverURL: url,
	}); err != nil {
		t.Fatal(err)
	}
	requireCover(t, d, b.ID)

	if _, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "2", Title: "Dune", // no CoverURL
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetBookCover(b.ID); ok {
		t.Fatal("stale cover survived a rematch to a candidate with no cover")
	}
}

// Re-matching to a candidate with a different cover_url must drop the old
// image rather than leave it captioned as belonging to the new edition.
func TestApply_InvalidatesCoverOnDifferentURL(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	urlA, _ := coverServer(t)
	urlB, _ := coverServer(t)
	m := newMatcher(d)

	if _, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "1", Title: "Dune", CoverURL: urlA,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AcceptCandidate(context.Background(), b.ID, model.Candidate{
		Provider: "manual", ProviderID: "2", Title: "Dune", CoverURL: urlB,
	}); err != nil {
		t.Fatal(err)
	}
	cov := requireCover(t, d, b.ID)
	if cov.SourceURL != urlB {
		t.Fatalf("cover source = %q, want the new candidate's %q", cov.SourceURL, urlB)
	}
}

// AcceptTopCandidates never calls a provider, and a cover fetch is exactly
// that: it must not happen even though the stored candidate names one.
func TestAcceptTopCandidates_NeverFetchesCover(t *testing.T) {
	d := testDB(t)
	lib := newLibrary(t, d)
	url, hits := coverServer(t)
	b := seedBookIn(t, d, lib, "Dune", model.StateNeedsReview,
		model.Candidate{Provider: "stub", ProviderID: "1", Title: "Dune", Authors: []string{"Frank Herbert"}, Score: 0.9, CoverURL: url})

	if _, err := newMatcher(d).AcceptTopCandidates(lib.ID, 0.7); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(hits); got != 0 {
		t.Fatalf("cover server was hit %d times; AcceptTopCandidates must never call out", got)
	}
	if _, ok, _ := d.GetBookCover(b.ID); ok {
		t.Fatal("AcceptTopCandidates must not store a cover it never fetched")
	}
}
