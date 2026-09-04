package matcher

import (
	"context"
	"path/filepath"
	"testing"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/metadata"
	"audiobookrenamer/internal/model"
)

// stubProvider returns a fixed candidate list regardless of the query.
type stubProvider struct {
	name string
	out  []model.Candidate
}

func (p *stubProvider) Name() string { return p.name }
func (p *stubProvider) Search(context.Context, metadata.Query) ([]model.Candidate, error) {
	return p.out, nil
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedBook(t *testing.T, d *db.DB) model.Book {
	t.Helper()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: filepath.Join(lib.RootPath, "Dune"),
		Layout: model.LayoutSingle, State: model.StateUnmatched,
		Title: "Dune", Author: "Frank Herbert",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newMatcher(d *db.DB, cands ...model.Candidate) *Matcher {
	reg := metadata.NewRegistryWithProviders(d, &stubProvider{name: "stub", out: cands})
	return New(d, reg, metadata.NewClient(d))
}

// A match that sets the author must also populate a DERIVED author_sort
// ("First Last" -> "Last, First") and record the provenance.
func TestMatchBook_SetsDerivedAuthorSort(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)

	m := newMatcher(d, model.Candidate{
		Provider: "stub", ProviderID: "1",
		Title: "Dune", Authors: []string{"Frank Herbert"}, Score: 0,
	})

	updated, _, err := m.MatchBook(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != model.StateMatched {
		t.Fatalf("state = %q, want matched", updated.State)
	}
	if updated.AuthorSort != "Herbert, Frank" {
		t.Fatalf("AuthorSort = %q, want %q", updated.AuthorSort, "Herbert, Frank")
	}
	if updated.AuthorSortSource != model.AuthorSortDerived {
		t.Fatalf("AuthorSortSource = %q, want %q", updated.AuthorSortSource, model.AuthorSortDerived)
	}

	// Persisted, not just returned.
	reloaded, err := d.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthorSort != "Herbert, Frank" || reloaded.AuthorSortSource != model.AuthorSortDerived {
		t.Fatalf("persisted AuthorSort=%q source=%q", reloaded.AuthorSort, reloaded.AuthorSortSource)
	}
}

// A hand-edited author_sort (source == "manual") must survive a later match.
func TestMatchBook_DoesNotOverwriteManualAuthorSort(t *testing.T) {
	d := testDB(t)
	b := seedBook(t, d)
	if err := d.SetBookAuthorSort(b.ID, "Custom Sort Name", model.AuthorSortManual); err != nil {
		t.Fatal(err)
	}

	m := newMatcher(d, model.Candidate{
		Provider: "stub", ProviderID: "1",
		Title: "Dune", Authors: []string{"Frank Herbert"},
	})
	if _, _, err := m.MatchBook(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}

	reloaded, err := d.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AuthorSort != "Custom Sort Name" {
		t.Fatalf("AuthorSort = %q, want the manual value to be preserved", reloaded.AuthorSort)
	}
	if reloaded.AuthorSortSource != model.AuthorSortManual {
		t.Fatalf("AuthorSortSource = %q, want manual", reloaded.AuthorSortSource)
	}
}
