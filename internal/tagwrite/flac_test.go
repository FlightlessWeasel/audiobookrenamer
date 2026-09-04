package tagwrite

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhowden/tag"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// makeFLAC writes a minimal valid FLAC (STREAMINFO + any extra blocks + two
// sync bytes of "frames") and returns its path.
func makeFLAC(t *testing.T, extra ...*flac.MetaDataBlock) string {
	t.Helper()
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:2], 4096) // min block size
	binary.BigEndian.PutUint16(si[2:4], 4096) // max block size
	// 20-bit sample rate | 3-bit (channels-1) | 5-bit (bps-1) | 36-bit samples
	binary.BigEndian.PutUint64(si[10:18], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36)

	meta := append([]*flac.MetaDataBlock{{Type: flac.StreamInfo, Data: si}}, extra...)
	f := &flac.File{Meta: meta, Frames: flac.FrameData{0xFF, 0xF8, 0, 0, 0, 0, 0, 0}}

	p := filepath.Join(t.TempDir(), "book.flac")
	if err := os.WriteFile(p, f.Marshal(), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func countBlocks(t *testing.T, path string) map[flac.BlockType]int {
	t.Helper()
	f, err := flac.ParseFile(path)
	if err != nil {
		t.Fatalf("re-parse %s: %v", path, err)
	}
	m := map[flac.BlockType]int{}
	for _, b := range f.Meta {
		m[b.Type]++
	}
	return m
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestFLAC_WriteReadRoundTrip(t *testing.T) {
	p := makeFLAC(t)

	if err := (flacWriter{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := flacWriter{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(fullSet) {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, fullSet)
	}

	// Independent check: the comment block literally carries the fields.
	raw, _ := os.ReadFile(p)
	for _, want := range []string{
		"TITLE=" + fullSet.Title,
		"ALBUM=" + fullSet.Album,
		"COMPOSER=" + fullSet.Composer,
		"SERIES=" + fullSet.Series,
		"SERIES-PART=" + fullSet.SeriesIndex,
		"ASIN=" + fullSet.ASIN,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("output missing %q", want)
		}
	}

	// Best-effort cross-check with a stock reader.
	if f, err := os.Open(p); err == nil {
		defer f.Close()
		if m, err := tag.ReadFrom(f); err == nil {
			if m.Title() != fullSet.Title || m.Album() != fullSet.Album {
				t.Fatalf("stock reader: title=%q album=%q", m.Title(), m.Album())
			}
		}
	}
}

func TestFLAC_FullRewriteDropsOldTags(t *testing.T) {
	oldVC := flacvorbis.New()
	_ = oldVC.Add("ARTIST", "Old Artist")
	_ = oldVC.Add("SERIES", "Wrong Series")
	_ = oldVC.Add("BOGUS", "junk")
	oldBlock := oldVC.Marshal()
	// A picture block that Read/rewrite must also drop.
	picVC := &flac.MetaDataBlock{Type: flac.Picture, Data: append(
		make([]byte, 32), tinyPNG(t)...)}

	p := makeFLAC(t, &oldBlock, picVC)

	want := TagSet{Title: "Elantris", Album: "Elantris", Artist: "Brandon Sanderson", Genre: "Audiobook"}
	if err := (flacWriter{}).Write(p, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := flacWriter{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("rewrite mismatch:\n got %+v\nwant %+v", got, want)
	}

	raw, _ := os.ReadFile(p)
	if bytes.Contains(raw, []byte("BOGUS=")) || bytes.Contains(raw, []byte("Wrong Series")) {
		t.Fatalf("stale comment survived the rewrite")
	}
	counts := countBlocks(t, p)
	if counts[flac.VorbisComment] != 1 {
		t.Fatalf("want exactly 1 VORBIS_COMMENT block, got %d", counts[flac.VorbisComment])
	}
	if counts[flac.Picture] != 0 {
		t.Fatalf("want 0 PICTURE blocks after a coverless rewrite, got %d", counts[flac.Picture])
	}
}

func TestFLAC_PreservesFramesStreamInfoAndSeekTable(t *testing.T) {
	seek := &flac.MetaDataBlock{Type: flac.SeekTable, Data: bytes.Repeat([]byte{0xAB}, 18)}
	p := makeFLAC(t, seek)

	before, err := flac.ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	streamInfo := append([]byte(nil), before.Meta[0].Data...)
	frames := append([]byte(nil), before.Frames...)

	if err := (flacWriter{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}

	after, err := flac.ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if after.Meta[0].Type != flac.StreamInfo || !bytes.Equal(after.Meta[0].Data, streamInfo) {
		t.Fatalf("STREAMINFO changed")
	}
	if !bytes.Equal(after.Frames, frames) {
		t.Fatalf("audio frames changed")
	}
	if countBlocks(t, p)[flac.SeekTable] != 1 {
		t.Fatalf("SEEKTABLE was not preserved")
	}
}

func TestFLAC_SemanticIdempotent(t *testing.T) {
	p := makeFLAC(t)
	var frames []byte
	if f, err := flac.ParseFile(p); err == nil {
		frames = append([]byte(nil), f.Frames...)
	}

	for i := 0; i < 3; i++ {
		if err := (flacWriter{}).Write(p, fullSet); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		got, err := flacWriter{}.Read(p)
		if err != nil {
			t.Fatalf("Read #%d: %v", i, err)
		}
		if !got.Equal(fullSet) {
			t.Fatalf("write #%d not idempotent:\n got %+v\nwant %+v", i, got, fullSet)
		}
		if f, _ := flac.ParseFile(p); !bytes.Equal(f.Frames, frames) {
			t.Fatalf("write #%d disturbed audio frames", i)
		}
		if c := countBlocks(t, p)[flac.VorbisComment]; c != 1 {
			t.Fatalf("write #%d produced %d comment blocks", i, c)
		}
	}
}

func TestFLAC_Cover(t *testing.T) {
	set := fullSet
	set.Cover = tinyPNG(t)
	set.CoverMIME = "image/png"

	p := makeFLAC(t)
	if err := (flacWriter{}).Write(p, set); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := flacWriter{}.Read(p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got.Cover, set.Cover) || got.CoverMIME != "image/png" {
		t.Fatalf("cover round-trip: %d bytes mime=%q", len(got.Cover), got.CoverMIME)
	}
	if !got.Equal(set) {
		t.Fatalf("cover set mismatch:\n got %+v\nwant %+v", got, set)
	}
	if countBlocks(t, p)[flac.Picture] != 1 {
		t.Fatalf("want exactly 1 PICTURE block")
	}
}

func TestFLAC_NoTempFileLeftBehind(t *testing.T) {
	p := makeFLAC(t)
	if err := (flacWriter{}).Write(p, fullSet); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			t.Fatalf("left scratch file behind: %s", e.Name())
		}
	}
}
