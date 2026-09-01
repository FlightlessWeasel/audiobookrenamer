package scanner

import (
	"os"
	"regexp"
	"strconv"
	"strings"

	"audiobookrenamer/internal/strutil"

	"github.com/dhowden/tag"
)

// fileTags is the subset of embedded metadata the scanner uses.
type fileTags struct {
	Title       string
	Album       string
	Artist      string
	AlbumArtist string
	Composer    string
	Year        int
	Track       int
	HasCover    bool
}

// digest renders the tag fields that feed book metadata (title/author/year via
// bookMetaFromTags, track via trackNumber) into a stable string, for inclusion
// in a book's scan fingerprint. HasCover is deliberately excluded: it does not
// change any persisted book field.
func (t fileTags) digest() string {
	return strings.Join([]string{
		t.Title, t.Album, t.Artist, t.AlbumArtist, t.Composer,
		strconv.Itoa(t.Year), strconv.Itoa(t.Track),
	}, "\x1f")
}

// readTags reads embedded tags from absPath. Unsupported containers or missing
// tags yield a zero fileTags and a nil error so a scan is never blocked.
func readTags(absPath string) fileTags {
	f, err := os.Open(absPath)
	if err != nil {
		return fileTags{}
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return fileTags{}
	}
	track, _ := m.Track()
	t := fileTags{
		Title:       strings.TrimSpace(m.Title()),
		Album:       strings.TrimSpace(m.Album()),
		Artist:      strings.TrimSpace(m.Artist()),
		AlbumArtist: strings.TrimSpace(m.AlbumArtist()),
		Composer:    strings.TrimSpace(m.Composer()),
		Year:        m.Year(),
		Track:       track,
	}
	if p := m.Picture(); p != nil && len(p.Data) > 0 {
		t.HasCover = true
	}
	return t
}

// bookMetaFromTags derives book-level fields from the first file's tags, with
// the folder name as a fallback title.
func bookMetaFromTags(t fileTags, folderName string) (title, author string, year int) {
	title = strutil.FirstNonEmpty(t.Album, t.Title, cleanFolderName(folderName))
	author = strutil.FirstNonEmpty(t.AlbumArtist, t.Artist, t.Composer)
	year = t.Year
	return
}

var trailingParenRe = regexp.MustCompile(`\s*[\(\[][^)\]]*[\)\]]\s*$`)

func cleanFolderName(name string) string {
	name = strings.TrimSpace(name)
	// Drop a single trailing "(2019)" / "[Unabridged]" style suffix.
	name = trailingParenRe.ReplaceAllString(name, "")
	return strings.TrimSpace(name)
}

// trackNumber picks a per-file track index: the file's own tag, else the
// sequence index grouping found in the filenames, else the file's position in
// the book. It deliberately does not re-parse the filename itself — a lone
// filename cannot say which of its digit runs is the track ("(2012)", "MP3"),
// only the whole set can, which is what sequenceIndex resolved during grouping.
func trackNumber(t fileTags, seqIndex, fallbackPos int) int {
	if t.Track > 0 {
		return t.Track
	}
	if seqIndex > 0 {
		return seqIndex
	}
	return fallbackPos
}
