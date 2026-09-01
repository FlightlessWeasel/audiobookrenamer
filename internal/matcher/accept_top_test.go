package matcher

import (
	"path/filepath"
	"testing"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

// seedBookIn creates one book in lib with the given state and stored candidates.
func seedBookIn(t *testing.T, d *db.DB, lib model.Library, name string, state model.BookState, cands ...model.Candidate) model.Book {
	t.Helper()
	b, err := d.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: filepath.Join(lib.RootPath, name),
		Layout: model.LayoutSingle, State: state, Title: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) > 0 {
		if err := d.ReplaceCandidates(b.ID, cands); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func newLibrary(t *testing.T, d *db.DB) model.Library {
	t.Helper()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func cand(id string, score float64) model.Candidate {
	return model.Candidate{
		Provider: "stub", ProviderID: id,
		Title: "Dune", Authors: []string{"Frank Herbert"}, Score: score,
	}
}

// AcceptTopCandidates takes the top stored candidate for every unmatched or
// needs-review book that clears the bar, and leaves everything else alone.
func TestAcceptTopCandidates(t *testing.T) {
	d := testDB(t)
	lib := newLibrary(t, d)

	clears := seedBookIn(t, d, lib, "Clears", model.StateNeedsReview, cand("1", 0.80), cand("2", 0.40))
	below := seedBookIn(t, d, lib, "Below", model.StateNeedsReview, cand("3", 0.55))
	noCands := seedBookIn(t, d, lib, "NoCands", model.StateUnmatched)
	// An already-matched book is not in scope and must keep its own match.
	done := seedBookIn(t, d, lib, "Done", model.StateMatched, cand("4", 0.99))

	m := newMatcher(d)
	out, err := m.AcceptTopCandidates(lib.ID, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", out.Accepted)
	}
	// Considered counts the books in scope, matched ones excluded.
	if out.Considered != 3 {
		t.Fatalf("considered = %d, want 3", out.Considered)
	}

	got := func(b model.Book) model.Book {
		t.Helper()
		r, err := d.GetBookBare(b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := got(clears); r.State != model.StateMatched || r.MatchedProviderID != "1" {
		t.Fatalf("cleared book: state=%q provider_id=%q, want matched/1", r.State, r.MatchedProviderID)
	}
	if r := got(below); r.State != model.StateNeedsReview {
		t.Fatalf("below-bar book: state=%q, want it left in needs_review", r.State)
	}
	if r := got(noCands); r.State != model.StateUnmatched {
		t.Fatalf("candidate-less book: state=%q, want it left unmatched", r.State)
	}
	if r := got(done); r.MatchedProviderID != "" {
		t.Fatalf("already-matched book was re-matched to %q", r.MatchedProviderID)
	}
}

// The bar is inclusive, and a candidate exactly on it is accepted.
func TestAcceptTopCandidates_BarIsInclusive(t *testing.T) {
	d := testDB(t)
	lib := newLibrary(t, d)
	b := seedBookIn(t, d, lib, "Exact", model.StateNeedsReview, cand("1", 0.7))

	out, err := newMatcher(d).AcceptTopCandidates(lib.ID, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", out.Accepted)
	}
	r, err := d.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != model.StateMatched {
		t.Fatalf("state = %q, want matched", r.State)
	}
}

// Unlike MatchBook, the bulk accept does not apply AutoPick's runner-up margin
// guard: a book held in review by an ambiguous runner-up is exactly what this
// is for.
func TestAcceptTopCandidates_IgnoresRunnerUpMargin(t *testing.T) {
	d := testDB(t)
	lib := newLibrary(t, d)
	// Two different works within AutoPick's 0.05 margin: AutoPick would refuse.
	tie := []model.Candidate{
		{Provider: "stub", ProviderID: "a", Title: "Dune", Authors: []string{"Frank Herbert"}, Score: 0.80},
		{Provider: "stub", ProviderID: "b", Title: "Emma", Authors: []string{"Jane Austen"}, Score: 0.79},
	}
	b := seedBookIn(t, d, lib, "Tied", model.StateNeedsReview, tie...)

	if _, err := newMatcher(d).AcceptTopCandidates(lib.ID, 0.7); err != nil {
		t.Fatal(err)
	}
	r, err := d.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != model.StateMatched || r.MatchedProviderID != "a" {
		t.Fatalf("state=%q provider_id=%q, want matched/a", r.State, r.MatchedProviderID)
	}
}

// An empty library id sweeps every library.
func TestAcceptTopCandidates_AllLibraries(t *testing.T) {
	d := testDB(t)
	a, b := newLibrary(t, d), newLibrary(t, d)
	seedBookIn(t, d, a, "A", model.StateNeedsReview, cand("1", 0.9))
	seedBookIn(t, d, b, "B", model.StateNeedsReview, cand("2", 0.9))

	out, err := newMatcher(d).AcceptTopCandidates("", 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted != 2 {
		t.Fatalf("accepted = %d, want 2 across both libraries", out.Accepted)
	}
}

// The outcome must classify every considered book into exactly one of
// Accepted / NoCandidates / BelowScore, so a caller can explain a 0-Accepted
// run (e.g. "these books have never been searched") instead of it reading as
// the feature silently doing nothing.
func TestAcceptTopCandidates_ClassifiesEveryConsideredBook(t *testing.T) {
	d := testDB(t)
	lib := newLibrary(t, d)

	seedBookIn(t, d, lib, "Clears", model.StateNeedsReview, cand("1", 0.80))
	seedBookIn(t, d, lib, "Below", model.StateNeedsReview, cand("2", 0.55))
	seedBookIn(t, d, lib, "NeverSearched", model.StateUnmatched)

	out, err := newMatcher(d).AcceptTopCandidates(lib.ID, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if out.Considered != 3 {
		t.Fatalf("considered = %d, want 3", out.Considered)
	}
	if out.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1", out.Accepted)
	}
	if out.BelowScore != 1 {
		t.Fatalf("below_score = %d, want 1", out.BelowScore)
	}
	if out.NoCandidates != 1 {
		t.Fatalf("no_candidates = %d, want 1", out.NoCandidates)
	}
	if sum := out.Accepted + out.BelowScore + out.NoCandidates; sum != out.Considered {
		t.Fatalf("buckets sum to %d, want considered %d", sum, out.Considered)
	}
}
