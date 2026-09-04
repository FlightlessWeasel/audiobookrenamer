package tagwrite

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
)

// fullSet is a TagSet exercising every field except the cover.
var fullSet = TagSet{
	Title:       "The Way of Kings, Part 3",
	Album:       "The Way of Kings",
	Artist:      "Brandon Sanderson",
	AlbumArtist: "Brandon Sanderson",
	Composer:    "Kate Reading, Michael Kramer",
	Genre:       "Audiobook",
	Year:        2010,
	Comment:     "",
	Series:      "The Stormlight Archive",
	SeriesIndex: "1",
	Subtitle:    "Book One of the Stormlight Archive",
	ASIN:        "B0036J92QQ",
	ISBN:        "9780765326355",
	Track:       3,
	TrackTotal:  45,
}

// fakeAudio returns deterministic non-ID3 bytes standing in for an MP3 payload.
func fakeAudio(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewSource(1))
	r.Read(b)
	// Ensure it never begins with an ID3v2 magic by accident.
	b[0], b[1], b[2] = 0xFF, 0xFB, 0x90
	return b
}

// makeMP3 writes "<id3v2 tag?><audio>" to a new file and returns its path. When
// seed is non-nil it is called to populate the leading tag; when nil the file
// has no tag at all.
func makeMP3(t *testing.T, audio []byte, seed func(*id3v2.Tag)) string {
	t.Helper()
	var buf bytes.Buffer
	if seed != nil {
		tg := id3v2.NewEmptyTag()
		tg.SetVersion(4)
		tg.SetDefaultEncoding(id3v2.EncodingUTF8)
		seed(tg)
		if _, err := tg.WriteTo(&buf); err != nil {
			t.Fatalf("seed WriteTo: %v", err)
		}
	}
	buf.Write(audio)
	p := filepath.Join(t.TempDir(), "book.mp3")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// audioPayload returns everything in the file after its ID3v2 tag.
func audioPayload(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(raw) < id3HeaderLen {
		return raw
	}
	return raw[id3v2TagLen(raw[:id3HeaderLen]):]
}

func TestMP3_WriteReadRoundTrip(t *testing.T) {
	audio := fakeAudio(8192)
	p := makeMP3(t, audio, nil)

	if err := (mp3Writer{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := mp3Writer{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(fullSet) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, fullSet)
	}

	// Cross-check that a stock reader sees the standard frames.
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		t.Fatalf("dhowden ReadFrom: %v", err)
	}
	if m.Title() != fullSet.Title || m.Album() != fullSet.Album ||
		m.Artist() != fullSet.Artist || m.Composer() != fullSet.Composer ||
		m.Year() != fullSet.Year || m.Genre() != fullSet.Genre {
		t.Fatalf("stock reader: title=%q album=%q artist=%q composer=%q year=%d genre=%q",
			m.Title(), m.Album(), m.Artist(), m.Composer(), m.Year(), m.Genre())
	}
	if n, _ := m.Track(); n != fullSet.Track {
		t.Fatalf("stock reader track = %d, want %d", n, fullSet.Track)
	}
}

func TestMP3_PreservesAudioPayload(t *testing.T) {
	audio := fakeAudio(64 * 1024)
	p := makeMP3(t, audio, func(tg *id3v2.Tag) { tg.SetArtist("Someone Else") })

	if err := (mp3Writer{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := audioPayload(t, p); !bytes.Equal(got, audio) {
		t.Fatalf("audio payload changed: got %d bytes, want %d", len(got), len(audio))
	}
}

func TestMP3_FullRewriteDropsPreexistingFrames(t *testing.T) {
	seed := func(tg *id3v2.Tag) {
		tg.SetArtist("Old Artist")
		tg.SetAlbum("Old Album")
		tg.AddCommentFrame(id3v2.CommentFrame{Encoding: id3v2.EncodingUTF8, Language: "eng", Text: "ripped by someone"})
		tg.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "FOO", Value: "bar"})
		tg.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "SERIES", Value: "Wrong Series"})
	}
	p := makeMP3(t, fakeAudio(4096), seed)

	want := TagSet{Title: "Elantris", Album: "Elantris", Artist: "Brandon Sanderson", Genre: "Audiobook"}
	if err := (mp3Writer{}).Write(p, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := mp3Writer{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("rewrite mismatch:\n got %+v\nwant %+v", got, want)
	}

	// Nothing from the seed tag may survive.
	tg, err := id3v2.Open(p, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	if v := txxxValue(tg, "FOO"); v != "" {
		t.Fatalf("stale TXXX:FOO survived: %q", v)
	}
	if len(tg.GetFrames("COMM")) != 0 {
		t.Fatalf("stale comment survived")
	}
	if got.Series != "" {
		t.Fatalf("stale series survived: %q", got.Series)
	}
}

func TestMP3_SemanticIdempotent(t *testing.T) {
	audio := fakeAudio(16 * 1024)
	p := makeMP3(t, audio, nil)

	for i := 0; i < 3; i++ {
		if err := (mp3Writer{}).Write(p, fullSet); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		got, err := mp3Writer{}.Read(p)
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if !got.Equal(fullSet) {
			t.Fatalf("write #%d not idempotent:\n got %+v\nwant %+v", i, got, fullSet)
		}
		if pl := audioPayload(t, p); !bytes.Equal(pl, audio) {
			t.Fatalf("write #%d disturbed audio payload", i)
		}
	}
}

func TestMP3_NoExistingTag(t *testing.T) {
	audio := fakeAudio(4096)
	p := makeMP3(t, audio, nil)

	// Sanity: fixture really has no leading tag.
	if got := audioPayload(t, p); !bytes.Equal(got, audio) {
		t.Fatalf("fixture already carries a tag")
	}
	if err := (mp3Writer{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := mp3Writer{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(fullSet) {
		t.Fatalf("mismatch:\n got %+v\nwant %+v", got, fullSet)
	}
	if pl := audioPayload(t, p); !bytes.Equal(pl, audio) {
		t.Fatalf("audio payload changed")
	}
}

func TestMP3_StripsTrailingID3v1(t *testing.T) {
	audio := fakeAudio(4096)

	// Build "<audio><128-byte ID3v1 tag>" with a recognisable old title.
	var v1 [128]byte
	copy(v1[0:3], "TAG")
	copy(v1[3:33], "Old ID3v1 Title")
	raw := append(append([]byte{}, audio...), v1[:]...)

	p := filepath.Join(t.TempDir(), "book.mp3")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (mp3Writer{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := audioPayload(t, p); !bytes.Equal(got, audio) {
		t.Fatalf("payload after strip = %d bytes, want the %d audio bytes with no ID3v1", len(got), len(audio))
	}
	final, _ := os.ReadFile(p)
	if bytes.Contains(final, []byte("Old ID3v1 Title")) {
		t.Fatalf("trailing ID3v1 tag survived the rewrite")
	}
}

func TestMP3_Cover(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), fakeAudio(512)...)
	set := fullSet
	set.Cover = png
	set.CoverMIME = "image/png"

	p := makeMP3(t, fakeAudio(2048), nil)
	if err := (mp3Writer{}).Write(p, set); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := mp3Writer{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got.Cover, png) || got.CoverMIME != "image/png" {
		t.Fatalf("cover round-trip: got %d bytes mime=%q", len(got.Cover), got.CoverMIME)
	}
	if !got.Equal(set) {
		t.Fatalf("cover set mismatch:\n got %+v\nwant %+v", got, set)
	}

	f, _ := os.Open(p)
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		t.Fatalf("dhowden ReadFrom: %v", err)
	}
	if pic := m.Picture(); pic == nil || len(pic.Data) == 0 {
		t.Fatalf("stock reader saw no embedded picture")
	}
}

func TestMP3_NoTempFileLeftBehind(t *testing.T) {
	p := makeMP3(t, fakeAudio(1024), nil)
	if err := (mp3Writer{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("left scratch file behind: %s", e.Name())
		}
	}
}

func TestID3v2TagLen(t *testing.T) {
	if got := id3v2TagLen([]byte("not an id3 header!!")); got != 0 {
		t.Fatalf("non-ID3 header: got %d, want 0", got)
	}

	// Header claiming 0x0000_0201 sync-safe = (2<<7)|1 = 257 frame bytes.
	hdr := []byte{'I', 'D', '3', 4, 0, 0x00, 0x00, 0x00, 0x02, 0x01}
	if got := id3v2TagLen(hdr); got != 10+257 {
		t.Fatalf("size decode: got %d, want %d", got, 10+257)
	}

	hdr[5] = 0x10 // footer present
	if got := id3v2TagLen(hdr); got != 10+257+10 {
		t.Fatalf("footer flag: got %d, want %d", got, 10+257+10)
	}
}
