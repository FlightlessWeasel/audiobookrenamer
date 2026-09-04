package tagwrite

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// mp3Writer rewrites the ID3v2 tag of an .mp3 file as ID3v2.4/UTF-8.
//
// Field mapping (a full rewrite: only these frames are written, everything the
// file carried before is dropped):
//
//	TagSet field   ID3v2.4 frame(s)
//	Title          TIT2
//	Album          TALB
//	Artist         TPE1
//	AlbumArtist    TPE2
//	Composer       TCOM
//	Genre          TCON
//	Year           TDRC
//	Track          TRCK ("n" or "n/total" when TrackTotal > 0)
//	Series         TXXX:SERIES
//	SeriesIndex    TXXX:SERIES-PART
//	Subtitle       TXXX:SUBTITLE
//	Composer       TXXX:NARRATOR  (mirror of TCOM; some readers only take this)
//	ASIN           TXXX:ASIN
//	ISBN           TXXX:ISBN
//	Comment        COMM (lang "eng", empty description)
//	Cover          APIC (front cover)
//
// Series is carried in a TXXX (user-defined text) frame rather than the ID3v2.4
// MVNM/MVIN movement frames: those are the more "correct" home for it, but the
// underlying library models neither, so a written MVNM would not survive its
// own round-trip. TXXX:SERIES is what Audiobookshelf and the common desktop
// taggers read anyway.
type mp3Writer struct{}

func (mp3Writer) Ext() string { return ".mp3" }

func (mp3Writer) Write(path string, ts TagSet) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	// Closed explicitly at the end of emit rather than only deferred: Windows
	// refuses to os.Rename over a file that still has an open handle, so src
	// must be shut before replaceFile renames the scratch file into place. The
	// deferred close stays as the error-path backstop and is harmless as a
	// double close.
	defer src.Close()

	fi, err := src.Stat()
	if err != nil {
		return err
	}

	// Bound the audio payload: everything between the leading ID3v2 tag and any
	// trailing 128-byte ID3v1 tag. A full rewrite replaces the ID3v2 tag and
	// drops the ID3v1 one entirely, so a reader that prefers ID3v1 can't be
	// left with the old title/author.
	//
	// The ID3v2 length is measured from its header directly: bogem parses the
	// tag but does not expose the byte offset it stopped at, and that offset is
	// exactly what a byte-preserving rewrite needs.
	var hdr [id3HeaderLen]byte
	n, err := io.ReadFull(src, hdr[:])
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return err
	}
	audioStart := int64(0)
	if n == id3HeaderLen {
		audioStart = id3v2TagLen(hdr[:])
	}
	audioEnd := fi.Size()
	if audioEnd-audioStart >= id3v1Len {
		var v1 [3]byte
		if _, err := src.ReadAt(v1[:], audioEnd-id3v1Len); err == nil && string(v1[:]) == "TAG" {
			audioEnd -= id3v1Len
		}
	}
	if audioEnd < audioStart {
		audioEnd = audioStart
	}
	if _, err := src.Seek(audioStart, io.SeekStart); err != nil {
		return err
	}

	tag := id3v2.NewEmptyTag()
	tag.SetVersion(4)
	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	applyMP3Frames(tag, ts)

	return replaceFile(path, func(w io.Writer) error {
		if _, err := tag.WriteTo(w); err != nil {
			return fmt.Errorf("write id3 tag: %w", err)
		}
		if _, err := io.CopyN(w, src, audioEnd-audioStart); err != nil {
			return fmt.Errorf("copy audio payload: %w", err)
		}
		return src.Close()
	})
}

func (mp3Writer) Read(path string) (TagSet, error) {
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return TagSet{}, err
	}
	defer tag.Close()

	ts := TagSet{
		Title:       strings.TrimSpace(tag.GetTextFrame("TIT2").Text),
		Album:       strings.TrimSpace(tag.GetTextFrame("TALB").Text),
		Artist:      strings.TrimSpace(tag.GetTextFrame("TPE1").Text),
		AlbumArtist: strings.TrimSpace(tag.GetTextFrame("TPE2").Text),
		Composer:    strings.TrimSpace(tag.GetTextFrame("TCOM").Text),
		Genre:       strings.TrimSpace(tag.GetTextFrame("TCON").Text),
		Year:        parseLeadingYear(tag.GetTextFrame("TDRC").Text),
		Subtitle:    txxxValue(tag, "SUBTITLE"),
		ASIN:        txxxValue(tag, "ASIN"),
		ISBN:        txxxValue(tag, "ISBN"),
	}
	ts.Series = txxxValue(tag, "SERIES")
	ts.SeriesIndex = txxxValue(tag, "SERIES-PART")
	ts.Track, ts.TrackTotal = parseTrack(tag.GetTextFrame("TRCK").Text)

	for _, f := range tag.GetFrames("COMM") {
		if cf, ok := f.(id3v2.CommentFrame); ok {
			ts.Comment = strings.TrimSpace(cf.Text)
			break
		}
	}
	if pic := frontCover(tag); pic != nil {
		ts.Cover = pic.Picture
		ts.CoverMIME = pic.MimeType
	}
	return ts, nil
}

// applyMP3Frames adds exactly the frames a TagSet calls for to an empty tag.
func applyMP3Frames(tag *id3v2.Tag, ts TagSet) {
	enc := id3v2.EncodingUTF8
	text := func(id, v string) {
		if v != "" {
			tag.AddTextFrame(id, enc, v)
		}
	}
	text("TIT2", ts.Title)
	text("TALB", ts.Album)
	text("TPE1", ts.Artist)
	text("TPE2", ts.AlbumArtist)
	text("TCOM", ts.Composer)
	text("TCON", ts.Genre)
	if ts.Year > 0 {
		text("TDRC", strconv.Itoa(ts.Year))
	}
	if ts.Track > 0 {
		if ts.TrackTotal > 0 {
			text("TRCK", fmt.Sprintf("%d/%d", ts.Track, ts.TrackTotal))
		} else {
			text("TRCK", strconv.Itoa(ts.Track))
		}
	}
	txxx := func(desc, v string) {
		if v == "" {
			return
		}
		tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
			Encoding: enc, Description: desc, Value: v,
		})
	}
	txxx("SERIES", ts.Series)
	txxx("SERIES-PART", ts.SeriesIndex)
	txxx("SUBTITLE", ts.Subtitle)
	txxx("NARRATOR", ts.Composer)
	txxx("ASIN", ts.ASIN)
	txxx("ISBN", ts.ISBN)

	if ts.Comment != "" {
		tag.AddCommentFrame(id3v2.CommentFrame{
			Encoding: enc, Language: "eng", Text: ts.Comment,
		})
	}
	if len(ts.Cover) > 0 && ts.CoverMIME != "" {
		tag.AddAttachedPicture(id3v2.PictureFrame{
			Encoding:    id3v2.EncodingISO,
			MimeType:    ts.CoverMIME,
			PictureType: id3v2.PTFrontCover,
			Description: "Cover",
			Picture:     ts.Cover,
		})
	}
}

const (
	id3HeaderLen = 10  // ID3v2 tag header (and footer) length
	id3v1Len     = 128 // trailing ID3v1 tag length
)

// id3v2TagLen returns the total on-disk length of the ID3v2 tag whose 10-byte
// header is hdr (header + frames + optional footer), or 0 when hdr is not an
// ID3v2 header. The size field is 4 sync-safe bytes (7 significant bits each).
func id3v2TagLen(hdr []byte) int64 {
	if len(hdr) < id3HeaderLen || string(hdr[0:3]) != "ID3" {
		return 0
	}
	size := int64(hdr[6]&0x7f)<<21 |
		int64(hdr[7]&0x7f)<<14 |
		int64(hdr[8]&0x7f)<<7 |
		int64(hdr[9]&0x7f)
	total := int64(id3HeaderLen) + size
	const footerFlag = 0x10 // header flags bit 4: a 10-byte footer follows
	if hdr[5]&footerFlag != 0 {
		total += id3HeaderLen
	}
	return total
}

func txxxValue(tag *id3v2.Tag, desc string) string {
	for _, f := range tag.GetFrames("TXXX") {
		if u, ok := f.(id3v2.UserDefinedTextFrame); ok && strings.EqualFold(u.Description, desc) {
			return strings.TrimSpace(u.Value)
		}
	}
	return ""
}

func frontCover(tag *id3v2.Tag) *id3v2.PictureFrame {
	var first *id3v2.PictureFrame
	for _, f := range tag.GetFrames("APIC") {
		pf, ok := f.(id3v2.PictureFrame)
		if !ok {
			continue
		}
		if pf.PictureType == id3v2.PTFrontCover {
			p := pf
			return &p
		}
		if first == nil {
			p := pf
			first = &p
		}
	}
	return first
}

func parseLeadingYear(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil {
		return 0
	}
	return y
}

func parseTrack(s string) (num, total int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0
	}
	n, t, found := strings.Cut(s, "/")
	num, _ = strconv.Atoi(strings.TrimSpace(n))
	if found {
		total, _ = strconv.Atoi(strings.TrimSpace(t))
	}
	return num, total
}
