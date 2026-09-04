package tagwrite

import (
	"errors"
	"testing"
)

func TestWriterFor(t *testing.T) {
	if w, err := WriterFor(".mp3"); err != nil || w.Ext() != ".mp3" {
		t.Fatalf(`WriterFor(".mp3") = %v, %v`, w, err)
	}
	if w, err := WriterFor(".MP3"); err != nil || w.Ext() != ".mp3" {
		t.Fatalf(`WriterFor(".MP3") = %v, %v`, w, err)
	}
	if w, err := WriterFor(".FLAC"); err != nil || w.Ext() != ".flac" {
		t.Fatalf(`WriterFor(".FLAC") = %v, %v`, w, err)
	}
	for _, ext := range []string{".m4b", ".m4a", ".M4B"} {
		if _, err := WriterFor(ext); err != nil {
			t.Fatalf("WriterFor(%q) = %v, want a writer", ext, err)
		}
	}

	// No dependable pure-Go writer exists for these; they stay unsupported.
	for _, ext := range []string{".ogg", ".opus", ".aac", ".wav", ".txt", ""} {
		if _, err := WriterFor(ext); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("WriterFor(%q) err = %v, want ErrUnsupported", ext, err)
		}
		if Supported(ext) {
			t.Fatalf("Supported(%q) = true, want false", ext)
		}
	}
	for _, ext := range []string{".mp3", ".flac", ".m4b", ".m4a"} {
		if !Supported(ext) {
			t.Fatalf("Supported(%q) = false", ext)
		}
	}
}

func TestTagSetEqual(t *testing.T) {
	base := fullSet

	if !base.Equal(fullSet) {
		t.Fatal("identical sets compare unequal")
	}

	mutators := map[string]func(*TagSet){
		"Title":       func(s *TagSet) { s.Title += "x" },
		"Album":       func(s *TagSet) { s.Album += "x" },
		"Artist":      func(s *TagSet) { s.Artist += "x" },
		"AlbumArtist": func(s *TagSet) { s.AlbumArtist += "x" },
		"Composer":    func(s *TagSet) { s.Composer += "x" },
		"Genre":       func(s *TagSet) { s.Genre = "Podcast" },
		"Year":        func(s *TagSet) { s.Year = 1999 },
		"Comment":     func(s *TagSet) { s.Comment = "note" },
		"Series":      func(s *TagSet) { s.Series += "x" },
		"SeriesIndex": func(s *TagSet) { s.SeriesIndex = "2" },
		"Subtitle":    func(s *TagSet) { s.Subtitle += "x" },
		"ASIN":        func(s *TagSet) { s.ASIN += "x" },
		"ISBN":        func(s *TagSet) { s.ISBN += "x" },
		"Track":       func(s *TagSet) { s.Track = 99 },
		"TrackTotal":  func(s *TagSet) { s.TrackTotal = 99 },
	}
	for name, mut := range mutators {
		got := base
		mut(&got)
		if base.Equal(got) {
			t.Fatalf("sets differing in %s compared equal", name)
		}
	}

	// Cover bytes matter.
	a := base
	a.Cover, a.CoverMIME = []byte("img"), "image/png"
	b := base
	b.Cover, b.CoverMIME = []byte("IMG"), "image/png"
	if a.Equal(b) {
		t.Fatal("different cover bytes compared equal")
	}

	// CoverMIME only matters when a cover is present.
	c := base
	c.CoverMIME = "image/png"
	if !base.Equal(c) {
		t.Fatal("CoverMIME difference with no cover should be ignored")
	}
	d := base
	d.Cover, d.CoverMIME = []byte("img"), "image/png"
	e := base
	e.Cover, e.CoverMIME = []byte("img"), "image/jpeg"
	if d.Equal(e) {
		t.Fatal("different CoverMIME with a cover present compared equal")
	}
}
