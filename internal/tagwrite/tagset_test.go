package tagwrite

import (
	"testing"

	"audiobookrenamer/internal/model"
)

func TestDesired_SingleFile(t *testing.T) {
	b := model.Book{
		Layout: model.LayoutSingle,
		Title:  "Elantris", Author: "Brandon Sanderson", Narrator: "Jack Garrett",
		Year: 2005, Series: "", SeriesIndex: "", Subtitle: "A Novel",
		ASIN: "B002V0PN2C", ISBN: "9780765350374",
	}
	got := Desired(b, model.BookFile{Ext: ".mp3"}, 1)

	want := TagSet{
		Title: "Elantris", Album: "Elantris",
		Artist: "Brandon Sanderson", AlbumArtist: "Brandon Sanderson",
		Composer: "Jack Garrett", Genre: "Audiobook", Year: 2005,
		Subtitle: "A Novel", ASIN: "B002V0PN2C", ISBN: "9780765350374",
	}
	if !got.Equal(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if got.Track != 0 || got.TrackTotal != 0 {
		t.Fatalf("single-file book got track %d/%d, want 0/0", got.Track, got.TrackTotal)
	}
}

func TestDesired_MultiFile(t *testing.T) {
	b := model.Book{
		Layout: model.LayoutMulti,
		Title:  "The Way of Kings", Author: "Brandon Sanderson",
		Series: "The Stormlight Archive", SeriesIndex: "1",
	}
	got := Desired(b, model.BookFile{Ext: ".mp3", Track: 3}, 45)

	if got.Title != "The Way of Kings, Part 3" {
		t.Fatalf("track title = %q", got.Title)
	}
	if got.Album != "The Way of Kings" {
		t.Fatalf("album = %q", got.Album)
	}
	if got.Track != 3 || got.TrackTotal != 45 {
		t.Fatalf("track = %d/%d, want 3/45", got.Track, got.TrackTotal)
	}
	if got.Series != "The Stormlight Archive" || got.SeriesIndex != "1" {
		t.Fatalf("series = %q #%q", got.Series, got.SeriesIndex)
	}
}

func TestDesired_MultiFileTotalOneOmitsTrackTotal(t *testing.T) {
	b := model.Book{Layout: model.LayoutMulti, Title: "Short Story"}
	got := Desired(b, model.BookFile{Track: 1}, 1)
	if got.Track != 1 {
		t.Fatalf("track = %d, want 1", got.Track)
	}
	if got.TrackTotal != 0 {
		t.Fatalf("track total = %d, want 0 when the book has one file", got.TrackTotal)
	}
}

func TestDesired_MultiFileNoTrackNumber(t *testing.T) {
	// A multi-file book whose file carries no track index gets no per-file
	// identity — it falls back to the plain book title, like a single file.
	b := model.Book{Layout: model.LayoutMulti, Title: "Anthology"}
	got := Desired(b, model.BookFile{Track: 0}, 12)
	if got.Title != "Anthology" || got.Track != 0 || got.TrackTotal != 0 {
		t.Fatalf("got title=%q track=%d/%d", got.Title, got.Track, got.TrackTotal)
	}
}

func TestDesired_MissingYearIsAbsent(t *testing.T) {
	got := Desired(model.Book{Layout: model.LayoutSingle, Title: "X"}, model.BookFile{}, 1)
	if got.Year != 0 {
		t.Fatalf("year = %d, want 0", got.Year)
	}
}

func TestDesired_GenreAlwaysAudiobook(t *testing.T) {
	got := Desired(model.Book{Layout: model.LayoutSingle, Title: "X"}, model.BookFile{}, 1)
	if got.Genre != "Audiobook" {
		t.Fatalf("genre = %q", got.Genre)
	}
}

func TestDesired_TrimsWhitespace(t *testing.T) {
	b := model.Book{
		Layout: model.LayoutSingle,
		Title:  "  Elantris  ", Author: "  Brandon Sanderson\t",
		Narrator: " Jack Garrett ", Series: "  ", SeriesIndex: " ",
	}
	got := Desired(b, model.BookFile{}, 1)
	if got.Title != "Elantris" || got.Album != "Elantris" {
		t.Fatalf("title/album not trimmed: %q / %q", got.Title, got.Album)
	}
	if got.Artist != "Brandon Sanderson" || got.Composer != "Jack Garrett" {
		t.Fatalf("artist/composer not trimmed: %q / %q", got.Artist, got.Composer)
	}
	if got.Series != "" || got.SeriesIndex != "" {
		t.Fatalf("blank series not normalised to empty: %q / %q", got.Series, got.SeriesIndex)
	}
}
