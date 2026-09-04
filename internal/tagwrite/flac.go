package tagwrite

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/go-flac/flacpicture"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// flacWriter rewrites the Vorbis comment and picture metadata of a .flac file.
//
// Full rewrite: the existing VORBIS_COMMENT block and every PICTURE block are
// dropped and rebuilt from the TagSet; STREAMINFO, SEEKTABLE, CUESHEET and
// APPLICATION blocks are kept untouched (they are stream structure, not tags),
// and the audio frames are preserved byte for byte. A single PADDING block is
// re-added so a later edit need not rewrite the whole file.
//
//	TagSet field   Vorbis comment
//	Title          TITLE
//	Album          ALBUM
//	Artist         ARTIST
//	AlbumArtist    ALBUMARTIST
//	Composer       COMPOSER
//	Genre          GENRE
//	Year           DATE
//	Track          TRACKNUMBER
//	TrackTotal     TRACKTOTAL
//	Comment        COMMENT
//	Series         SERIES
//	SeriesIndex    SERIES-PART
//	Subtitle       SUBTITLE
//	ASIN           ASIN
//	ISBN           ISBN
//	Cover          METADATA_BLOCK_PICTURE (type 3, front cover)
type flacWriter struct{}

func (flacWriter) Ext() string { return ".flac" }

// flacPadding is the size of the PADDING block left after a rewrite.
const flacPadding = 1024

func (flacWriter) Write(path string, ts TagSet) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return err
	}

	// Keep everything that is not a tag; drop the blocks we are about to
	// rebuild. STREAMINFO stays at index 0 either way (it is never in the
	// dropped set and is always first in a valid stream).
	kept := f.Meta[:0]
	for _, b := range f.Meta {
		switch b.Type {
		case flac.VorbisComment, flac.Picture, flac.Padding:
			// rebuilt below
		default:
			kept = append(kept, b)
		}
	}
	f.Meta = kept

	vc := flacvorbis.New()
	add := func(field, v string) error {
		if v == "" {
			return nil
		}
		return vc.Add(field, v)
	}
	for _, kv := range []struct{ field, v string }{
		{flacvorbis.FIELD_TITLE, ts.Title},
		{flacvorbis.FIELD_ALBUM, ts.Album},
		{flacvorbis.FIELD_ARTIST, ts.Artist},
		{"ALBUMARTIST", ts.AlbumArtist},
		{"COMPOSER", ts.Composer},
		{flacvorbis.FIELD_GENRE, ts.Genre},
		{flacvorbis.FIELD_DATE, yearString(ts.Year)},
		{flacvorbis.FIELD_TRACKNUMBER, trackString(ts.Track)},
		{"TRACKTOTAL", trackString(ts.TrackTotal)},
		{"COMMENT", ts.Comment},
		{"SERIES", ts.Series},
		{"SERIES-PART", ts.SeriesIndex},
		{"SUBTITLE", ts.Subtitle},
		{"ASIN", ts.ASIN},
		{"ISBN", ts.ISBN},
	} {
		if err := add(kv.field, kv.v); err != nil {
			return fmt.Errorf("vorbis %s: %w", kv.field, err)
		}
	}
	vcBlock := vc.Marshal()
	f.Meta = append(f.Meta, &vcBlock)

	if len(ts.Cover) > 0 && ts.CoverMIME != "" {
		// A cover that will not decode is skipped, not fatal: the text tags are
		// still worth writing, and the pipeline validates cover bytes before it
		// ever reaches a writer.
		if pic, err := flacpicture.NewFromImageData(
			flacpicture.PictureTypeFrontCover, "Cover", ts.Cover, ts.CoverMIME,
		); err == nil {
			picBlock := pic.Marshal()
			f.Meta = append(f.Meta, &picBlock)
		}
	}

	f.Meta = append(f.Meta, &flac.MetaDataBlock{
		Type: flac.Padding,
		Data: make([]byte, flacPadding),
	})

	data := f.Marshal()
	return replaceFile(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func (flacWriter) Read(path string) (TagSet, error) {
	f, err := flac.ParseFile(path)
	if err != nil {
		return TagSet{}, err
	}

	var ts TagSet
	for _, b := range f.Meta {
		switch b.Type {
		case flac.VorbisComment:
			vc, err := flacvorbis.ParseFromMetaDataBlock(*b)
			if err != nil {
				return TagSet{}, fmt.Errorf("parse vorbis comment: %w", err)
			}
			ts.Title = flacGet(vc, flacvorbis.FIELD_TITLE)
			ts.Album = flacGet(vc, flacvorbis.FIELD_ALBUM)
			ts.Artist = flacGet(vc, flacvorbis.FIELD_ARTIST)
			ts.AlbumArtist = flacGet(vc, "ALBUMARTIST")
			ts.Composer = flacGet(vc, "COMPOSER")
			ts.Genre = flacGet(vc, flacvorbis.FIELD_GENRE)
			ts.Year = parseLeadingYear(flacGet(vc, flacvorbis.FIELD_DATE))
			ts.Track = atoi(flacGet(vc, flacvorbis.FIELD_TRACKNUMBER))
			ts.TrackTotal = atoi(flacGet(vc, "TRACKTOTAL"))
			ts.Comment = flacGet(vc, "COMMENT")
			ts.Series = flacGet(vc, "SERIES")
			ts.SeriesIndex = flacGet(vc, "SERIES-PART")
			ts.Subtitle = flacGet(vc, "SUBTITLE")
			ts.ASIN = flacGet(vc, "ASIN")
			ts.ISBN = flacGet(vc, "ISBN")
		case flac.Picture:
			pic, err := flacpicture.ParseFromMetaDataBlock(*b)
			if err != nil {
				return TagSet{}, fmt.Errorf("parse picture: %w", err)
			}
			if len(ts.Cover) == 0 || pic.PictureType == flacpicture.PictureTypeFrontCover {
				ts.Cover = pic.ImageData
				ts.CoverMIME = pic.MIME
			}
		}
	}
	return ts, nil
}

func flacGet(vc *flacvorbis.MetaDataBlockVorbisComment, key string) string {
	vals, err := vc.Get(key)
	if err != nil || len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
}

func yearString(y int) string {
	if y <= 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func trackString(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
