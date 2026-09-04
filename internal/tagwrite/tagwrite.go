// Package tagwrite writes a complete, canonical set of embedded metadata tags
// to an audio file, replacing whatever tags the file already carried.
//
// It is a full rewrite, not a merge: a writer discards every existing tag in
// the container and writes exactly the fields of a TagSet. A field left at its
// zero value is written as absent, not blank, so a rewrite of a book whose
// series is unknown leaves no stale SERIES frame behind. The field-to-frame (or
// -atom) mapping follows the conventions Audiobookshelf reads, so a retagged
// library is still picked up correctly downstream — see the per-container
// files for the exact mapping tables.
//
// Supported containers: .mp3 (ID3v2.4), .m4b/.m4a (MP4 iTunes metadata), and
// .flac (Vorbis comments). The remaining audiobook extensions the scanner
// accepts — .ogg, .opus, .aac, .wav — have no dependable pure-Go tag writer;
// WriterFor returns ErrUnsupported for them so a caller skips the file with a
// visible reason instead of failing the whole organize run.
package tagwrite

import (
	"bytes"
	"errors"
	"strings"
)

// ErrUnsupported is returned by WriterFor for a container this package cannot
// write tags to. It is not a failure of the run: the caller is expected to
// leave the file's tags untouched and record the reason in the plan.
var ErrUnsupported = errors.New("tagwrite: no tag writer for this container")

// TagSet is the complete canonical metadata for one audio file. Zero-valued
// fields are written as absent. All string fields are stored already trimmed.
type TagSet struct {
	// Title is this file's own title: the book title for a single-file book,
	// or "<book title>, Part N" for one track of a multi-file book.
	Title string
	// Album is the book title — the "album" in every tagging scheme.
	Album string
	// Artist and AlbumArtist both carry the author. Some readers key on one,
	// some the other, so a full rewrite sets both to the same value.
	Artist      string
	AlbumArtist string
	// Composer carries the narrator (the Audiobookshelf convention).
	Composer string
	// Genre is "Audiobook" for everything this app writes; it is a field
	// rather than a constant so tests and future callers can override it.
	Genre string
	// Year is the publication year; 0 means absent.
	Year int
	// Comment is normally empty — a full rewrite clears stray comments.
	Comment string

	Series      string
	SeriesIndex string
	Subtitle    string
	ASIN        string
	ISBN        string

	// Track is this file's 1-based position; 0 means absent (single-file book).
	Track int
	// TrackTotal is the number of files in the book; 0 means "do not write a
	// total" (single-file book, or an unknown count).
	TrackTotal int

	// Cover is the front-cover image bytes (JPEG or PNG), or nil. CoverMIME is
	// the matching media type ("image/jpeg" or "image/png") and must be set
	// whenever Cover is non-empty.
	Cover     []byte
	CoverMIME string
}

// Equal reports whether a and b would produce the same embedded tags. It backs
// the planner's no-op check: a file whose current canonical tags already Equal
// the desired set is left untouched, keeping organize runs idempotent. The
// cover is compared by its bytes; CoverMIME is only significant when a cover is
// present.
func (a TagSet) Equal(b TagSet) bool {
	if !bytes.Equal(a.Cover, b.Cover) {
		return false
	}
	if a.HasCover() && a.CoverMIME != b.CoverMIME {
		return false
	}
	return a.Title == b.Title &&
		a.Album == b.Album &&
		a.Artist == b.Artist &&
		a.AlbumArtist == b.AlbumArtist &&
		a.Composer == b.Composer &&
		a.Genre == b.Genre &&
		a.Year == b.Year &&
		a.Comment == b.Comment &&
		a.Series == b.Series &&
		a.SeriesIndex == b.SeriesIndex &&
		a.Subtitle == b.Subtitle &&
		a.ASIN == b.ASIN &&
		a.ISBN == b.ISBN &&
		a.Track == b.Track &&
		a.TrackTotal == b.TrackTotal
}

// HasCover reports whether the set carries cover-image bytes.
func (a TagSet) HasCover() bool { return len(a.Cover) > 0 }

// Writer rewrites the embedded tags of one container format.
type Writer interface {
	// Ext is the lowercase extension (leading dot) this writer handles.
	Ext() string
	// Write replaces every embedded tag in the file at path with ts. It writes
	// through a fsynced temporary file in the same directory and renames it
	// into place, so an interrupted write can never truncate or corrupt the
	// original — the file either still has its old tags or has all the new
	// ones. The audio payload is preserved byte for byte.
	Write(path string, ts TagSet) error
	// Read returns the file's current tags as this package would write them,
	// so a caller can diff against a desired TagSet with Equal. Fields the
	// container does not carry come back zero.
	Read(path string) (TagSet, error)
}

// WriterFor returns the Writer for a file extension (lowercase, leading dot, as
// stored on model.BookFile.Ext), or ErrUnsupported.
func WriterFor(ext string) (Writer, error) {
	switch strings.ToLower(ext) {
	case ".mp3":
		return mp3Writer{}, nil
	case ".m4b", ".m4a":
		return mp4Writer{}, nil
	case ".flac":
		return flacWriter{}, nil
	default:
		return nil, ErrUnsupported
	}
}

// Supported reports whether WriterFor has a writer for ext.
func Supported(ext string) bool {
	_, err := WriterFor(ext)
	return err == nil
}
