package matching

import (
	"testing"

	"audiobookrenamer/internal/model"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"The Final Empire":   "final empire",
		"Café  &  Books":     "cafe and books",
		"A Game of Thrones":  "game of thrones",
		"Hitchhiker's Guide": "hitchhikers guide",
		"Dune: Messiah":      "dune messiah",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTitle_DropsSubtitle(t *testing.T) {
	if got := NormalizeTitle("Mistborn: The Final Empire"); got != "mistborn" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeTitle("Project Hail Mary (Unabridged)"); got != "project hail mary" {
		t.Errorf("got %q", got)
	}
}

func TestSeriesIndexEqual(t *testing.T) {
	pairs := [][2]string{{"3", "03"}, {"III", "3"}, {"3.0", "3"}, {"iv", "4"}}
	for _, p := range pairs {
		if !SeriesIndexEqual(p[0], p[1]) {
			t.Errorf("SeriesIndexEqual(%q,%q) = false", p[0], p[1])
		}
	}
	if SeriesIndexEqual("2", "3") {
		t.Error("2 != 3")
	}
	if SeriesIndexEqual("", "") {
		t.Error("empty indexes are not equal")
	}
}

func TestScoreAndAutoPick(t *testing.T) {
	book := model.Book{Title: "The Final Empire", Author: "Brandon Sanderson", Year: 2006}

	good := model.Candidate{
		Provider: "audible", ProviderID: "A1",
		Title: "The Final Empire", Subtitle: "Mistborn, Book 1",
		Authors: []string{"Brandon Sanderson"}, Year: 2006,
	}
	near := model.Candidate{
		Provider: "googlebooks", ProviderID: "G1",
		Title: "The Well of Ascension", Authors: []string{"Brandon Sanderson"}, Year: 2007,
	}
	bad := model.Candidate{
		Provider: "openlibrary", ProviderID: "O1",
		Title: "Pride and Prejudice", Authors: []string{"Jane Austen"}, Year: 1813,
	}

	ranked := Rank(book, []model.Candidate{near, bad, good})
	if ranked[0].ProviderID != "A1" {
		t.Fatalf("expected good candidate first, got %+v", ranked[0])
	}
	if ranked[0].Score < 0.9 {
		t.Errorf("exact match should score high, got %.3f", ranked[0].Score)
	}
	if s := Score(book, bad); s > 0.2 {
		t.Errorf("unrelated book should score low, got %.3f", s)
	}

	if _, ok := AutoPick(ranked, 0.85); !ok {
		t.Error("expected auto-pick for a clear exact match")
	}

}

func TestAutoPick_MarginRules(t *testing.T) {
	mk := func(title, author string, score float64) model.Candidate {
		return model.Candidate{Title: title, Authors: []string{author}, Score: score}
	}

	// Different works, near-tied scores -> ambiguous, no pick.
	diff := []model.Candidate{
		mk("The Final Empire", "Brandon Sanderson", 0.90),
		mk("The Well of Ascension", "Brandon Sanderson", 0.88),
	}
	if _, ok := AutoPick(diff, 0.85); ok {
		t.Error("near-tied different works should not auto-pick")
	}

	// Same work from two providers, near-tied -> consensus, pick.
	same := []model.Candidate{
		mk("Project Hail Mary", "Andy Weir", 1.0),
		mk("Project Hail Mary", "Andy Weir", 1.0),
	}
	if _, ok := AutoPick(same, 0.85); !ok {
		t.Error("agreeing providers should auto-pick despite zero margin")
	}

	// Clear leader -> pick.
	lead := []model.Candidate{
		mk("Project Hail Mary", "Andy Weir", 0.95),
		mk("Artemis", "Andy Weir", 0.60),
	}
	if _, ok := AutoPick(lead, 0.85); !ok {
		t.Error("clear leader should auto-pick")
	}

	// Below threshold -> no pick.
	if _, ok := AutoPick([]model.Candidate{mk("x", "y", 0.5)}, 0.85); ok {
		t.Error("sub-threshold should not auto-pick")
	}
}
