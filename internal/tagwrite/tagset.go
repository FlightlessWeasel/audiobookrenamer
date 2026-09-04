package tagwrite

import (
	"fmt"
	"strings"

	"audiobookrenamer/internal/model"
)

// Genre is the value written to the genre frame/atom of every file this app
// tags.
const Genre = "Audiobook"

// Desired builds the canonical TagSet for one file of a book from its accepted
// metadata. total is the number of audio files in the book; a value of 0 or 1
// suppresses the track-total (a single-file book carries no track frame at
// all).
//
// It is the single source of truth for how domain metadata maps onto tags. The
// cover image is not set here — the organize pipeline injects it (the same
// cover on every file of a multi-file book) after calling Desired.
func Desired(b model.Book, f model.BookFile, total int) TagSet {
	title := strings.TrimSpace(b.Title)
	ts := TagSet{
		Title:       title,
		Album:       title,
		Artist:      strings.TrimSpace(b.Author),
		AlbumArtist: strings.TrimSpace(b.Author),
		Composer:    strings.TrimSpace(b.Narrator),
		Genre:       Genre,
		Year:        b.Year,
		Series:      strings.TrimSpace(b.Series),
		SeriesIndex: strings.TrimSpace(b.SeriesIndex),
		Subtitle:    strings.TrimSpace(b.Subtitle),
		ASIN:        strings.TrimSpace(b.ASIN),
		ISBN:        strings.TrimSpace(b.ISBN),
	}
	// Only a multi-file book gets per-file track identity. A distinct track
	// title ("<book>, Part 3") is more useful in a dumb player than repeating
	// the book title on every row, and the track number frame still carries
	// the ordering.
	if b.Layout == model.LayoutMulti && f.Track > 0 {
		ts.Title = fmt.Sprintf("%s, Part %d", title, f.Track)
		ts.Track = f.Track
		if total > 1 {
			ts.TrackTotal = total
		}
	}
	return ts
}
